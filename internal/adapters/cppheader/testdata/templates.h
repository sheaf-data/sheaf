// Templated class plus an explicit specialization that is skipped.

/// Primary template.
template <typename T>
class Box {
 public:
  /// Get the boxed value.
  T Get();
};

// Explicit specialization — not emitted as a separate element.
template <>
class Box<int> {
 public:
  int Get();
};
