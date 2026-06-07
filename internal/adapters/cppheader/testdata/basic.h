// A trivial header with one class and three public methods.

/// Server runs an RPC loop.
class Server {
 public:
  /// Construct a Server.
  Server();

  /// Start begins serving.
  void Start();

  /// Stop halts the loop.
  void Stop();
};
