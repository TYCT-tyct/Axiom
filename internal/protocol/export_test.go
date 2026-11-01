package protocol

// Left is what the budget has left, for tests to see that streams give back what they drew.
func (m *Memory) Left() int64 { return m.left.Load() }

// InjectServerFunctionForTest exposes the endpoint-format function injection to protocol_test.
func InjectServerFunctionForTest(f Format, body []byte) ([]byte, error) {
	return injectServerFunction(f, body)
}
