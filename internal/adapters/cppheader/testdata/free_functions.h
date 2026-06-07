// File-scope free functions with various return-type shapes.

#include <string>
#include <vector>

/// Plain int return.
int plain_int();

/// Pointer return.
char* return_ptr();

/// Reference return.
int& return_ref(int& seed);

/// Const reference return.
const std::string& return_cref();

/// Templated return.
std::vector<int> return_vec();
