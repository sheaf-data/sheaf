// Enums of every shape we care about.

/// Plain enum.
enum Color {
  RED,
  GREEN,
  BLUE,
};

/// Scoped enum (enum class).
enum class Status : int {
  kOk = 0,
  kFailed = 1,
};

// Anonymous enums are skipped.
enum {
  ANON_A,
  ANON_B,
};
