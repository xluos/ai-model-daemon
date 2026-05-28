package runtime

// Runtime is the interface every inference backend must implement.
type Runtime interface {
	Kind() string
	Ensure(modelID string, opts any) error
	Stop() error
	IsReady() bool
	LoadedModel() string
	ProxyURL() string
	AcquireSlot()
	ReleaseSlot()
	InFlightCount() int64
	MaxParallel() int
	Logs() []string
	Status() any
}
