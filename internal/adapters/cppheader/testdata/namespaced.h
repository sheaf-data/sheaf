// Nested namespaces.

namespace foo {
namespace bar {

/// Baz lives in foo::bar.
class Baz {
 public:
  /// Quux does something.
  int Quux(int n);
};

}  // namespace bar
}  // namespace foo
