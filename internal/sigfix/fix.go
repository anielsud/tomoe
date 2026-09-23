package sigfix

/*
#include <signal.h>

// fixOnnxSignalHandler re-installs the SIGSEGV handler with SA_ONSTACK.
// ONNX Runtime installs a SIGSEGV handler without SA_ONSTACK, which
// conflicts with Go's signal handling and causes "non-Go code set up
// signal handler without SA_ONSTACK flag" fatal errors.
// Must be called after every sherpa-onnx object creation.
static void fixOnnxSignalHandler() {
	struct sigaction sa;
	if (sigaction(SIGSEGV, NULL, &sa) == 0) {
		if (sa.sa_handler != SIG_DFL && sa.sa_handler != SIG_IGN) {
			sa.sa_flags |= SA_ONSTACK;
			sigaction(SIGSEGV, &sa, NULL);
		}
	}
}
*/
import "C"

// AfterSherpa patches ONNX Runtime's signal handlers to be compatible
// with Go's runtime. Must be called after every sherpa-onnx object
// creation (recognizer, VAD, embedder, etc.) since each creation may
// reinstall the handler without SA_ONSTACK.
func AfterSherpa() {
	C.fixOnnxSignalHandler()
}
