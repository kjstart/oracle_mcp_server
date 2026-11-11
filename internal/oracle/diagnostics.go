package oracle

import "context"

type stageLoggerKey struct{}

// WithStageLogger attaches request-scoped, SQL-free operation diagnostics.
func WithStageLogger(ctx context.Context, logger func(string, string)) context.Context {
	return context.WithValue(ctx, stageLoggerKey{}, logger)
}

func traceStage(ctx context.Context, stage, detail string) {
	if logger, ok := ctx.Value(stageLoggerKey{}).(func(string, string)); ok {
		logger(stage, detail)
	}
}

func resultStatus(err error) string {
	if err != nil {
		return "status=error"
	}
	return "status=success"
}
