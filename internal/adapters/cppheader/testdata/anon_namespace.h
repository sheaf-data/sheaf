// Declarations inside an anonymous namespace are translation-unit
// private and must be skipped.

namespace {

class HiddenClass {
 public:
  void HiddenMethod();
};

int hidden_free();

}  // anonymous namespace
