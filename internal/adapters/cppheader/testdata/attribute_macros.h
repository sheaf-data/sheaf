// Attribute macros listed in ignored_attribute_macros should not
// poison the next-token-is-declaration heuristic.

#define ABSL_DEPRECATED(msg)
#define PW_NO_LINT

/// Free function wrapped in attribute macros.
ABSL_DEPRECATED("use NewThing instead") PW_NO_LINT int OldThing();

/// Class preceded by an attribute macro.
PW_NO_LINT class Widget {
 public:
  /// Method preceded by an attribute macro.
  ABSL_DEPRECATED("use NewMethod") void OldMethod();
};
