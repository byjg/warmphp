<?php

// CakePHP in callable mode: one Application for the worker. This is Cake\Http\Server::run()
// without its bootstrap() call, which runs once here instead of on every request.

use App\Application;
use Cake\Core\Configure;
use Cake\Error\Renderer\HtmlErrorRenderer;
use Cake\Error\Renderer\WebExceptionRenderer;
use Cake\Http\FlashMessage;
use Cake\Http\MiddlewareQueue;
use Cake\Http\Runner;
use Cake\Http\Server;
use Cake\Http\ServerRequestFactory;
use Cake\Http\Session;

$root = getenv('PROJECT_ROOT');
require $root . '/vendor/autoload.php';

/**
 * Cake\Http\Session never starts a PHP session when PHP_SAPI is "cli".
 */
final class WorkerSession extends Session
{
    public function __construct(array $config = [])
    {
        parent::__construct($config);
        $this->_isCLI = false;
    }
}

// BaseApplication::handle() adds the request to the DI container with add(), which League
// Container appends, and get() returns the FIRST definition: a persistent container would
// give every controller the first request ever served. Each request gets a new container.
$app = new class ($root . '/config') extends Application {
    public function resetForWorker(): void
    {
        $this->container = null;
        $this->controllerFactory = null;
    }
};
$server = new Server($app);
$app->bootstrap();
$app->pluginBootstrap();

// PHP_SAPI is "cli": without these, errors render as console text
Configure::write('Error.errorRenderer', HtmlErrorRenderer::class);
Configure::write('Error.exceptionRenderer', WebExceptionRenderer::class);

return static function () use ($app, $server): void {
    $app->resetForWorker();

    // CAKEPHP_INPUT is CakePHP's own way to pass the body instead of php://input
    $request = ServerRequestFactory::fromGlobals(['CAKEPHP_INPUT' => PhpWorker::input()] + $_SERVER);
    // Same session config as fromGlobals(); FlashMessage keeps its own reference to the session
    $session = WorkerSession::create((array)Configure::read('Session') + [
        'defaults' => 'php',
        'cookiePath' => $request->getAttribute('webroot'),
    ]);
    $request = $request->withAttribute('session', $session)->withAttribute('flash', new FlashMessage($session));

    $middleware = $app->pluginMiddleware($app->middleware(new MiddlewareQueue([], $app->getContainer())));
    $server->dispatchEvent('Server.buildMiddleware', ['middleware' => $middleware]);
    $response = (new Runner())->run($middleware, $request, $app);
    $request->getSession()->close();

    $server->emit($response);
};
