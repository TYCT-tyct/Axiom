package protocol

// nativeConversionMemory is the part of a native-to-native relay's buffers the base allowance does not cover: it reads
// one format's events and writes another's through the Chat chunks an existing conversion makes (nativeConversions), so
// two conversions' buffers are alive at once. The samples hold at most 68 KiB, read one byte at a time, which is what
// the extra covers; TestConvertStreamsBetweenNativeFormats runs them with no node budget at all.
const nativeConversionMemory = 16 << 10

// nativeConversions are the relay formats of Messages and Responses streams converted into each other (README.md
// 「跨格式的流」): each reads the upstream's events with its own reader, then converts them through the Chat chunks the
// existing conversions write, so the mapping is the one Chat uses and cannot drift from it.
var (
	responsesToMessagesFormat = relayFormat{newResponsesToMessages, relayFormats[Responses].limit, true}
	messagesToResponsesFormat = relayFormat{newMessagesToResponses, relayFormats[Messages].limit, true}
)

// formatFor picks the conversion that relays upstream to client, and whether it is the native pair, which holds two
// conversions' buffers at once. It returns nil when the pair cannot stream.
func formatFor(client, upstream Format) (*relayFormat, bool) {
	if format, ok := chatFormatFor(client, upstream); ok {
		return format, false
	}
	switch {
	case client == Messages && upstream == Responses:
		return &responsesToMessagesFormat, true
	case client == Responses && upstream == Messages:
		return &messagesToResponsesFormat, true
	}
	return nil, false
}

// chatFormatFor is the conversion Chat makes possible: the same format, or one side Chat's.
func chatFormatFor(client, upstream Format) (*relayFormat, bool) {
	switch {
	case client == upstream && int(upstream) < len(relayFormats):
		return &relayFormats[upstream], true
	case client == Chat && int(upstream) < len(chatConversions) && chatConversions[upstream].newUpstream != nil:
		return &chatConversions[upstream], true
	case upstream == Chat && int(client) < len(fromChatConversions) && fromChatConversions[client].newUpstream != nil:
		return &fromChatConversions[client], true
	}
	return nil, false
}
