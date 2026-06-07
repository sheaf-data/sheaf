// Access specifiers — only public methods emit.

/// Cache implements a small key-value store.
class Cache {
 public:
  /// Get returns a value.
  int Get(int key);

  /// Put stores a value.
  void Put(int key, int value);

 private:
  /// rehash_ rebuilds the table.
  void rehash_();

  int size_;

 protected:
  /// on_resize_ is a hook for subclasses.
  void on_resize_();
};
