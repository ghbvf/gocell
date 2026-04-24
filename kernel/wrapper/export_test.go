package wrapper

// ResetTracerForTest restores the package-level tracer to panicIfNotSetTracer.
// Call via t.Cleanup(wrapper.ResetTracerForTest) after any test that calls
// wrapper.SetTracer, so subsequent tests see the unset (panic) state.
var ResetTracerForTest = resetTracerForTest
