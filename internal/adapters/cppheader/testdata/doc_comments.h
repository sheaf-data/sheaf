// Exercises both /// and /** ... */ comment styles.

/// Triple-slash doc on a class.
class WithTripleSlash {
 public:
  /// Triple-slash doc on a method.
  void Method();
};

/**
 * Javadoc doc on a class.
 */
class WithJavadoc {
 public:
  /**
   * Javadoc doc on a method.
   */
  void Method();
};

// A plain // comment is NOT a doc comment and should not attach.
class NoDoc {
 public:
  void Method();
};
