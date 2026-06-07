#ifndef TEST_MACROS_H_
#define TEST_MACROS_H_

/// Log at DEBUG level.
#define PW_LOG_DEBUG(...) pw::log::Log(0, __VA_ARGS__)

/// Bare define, no args.
#define PW_LOG_LEVEL_INFO 1

#endif  // TEST_MACROS_H_
