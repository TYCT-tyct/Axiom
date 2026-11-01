package protocol

import "encoding/json/jsontext" //nolint:depguard // as in canon.go

// contentNames is the shared mapping for request content in both directions.
var contentNames = map[Format]map[string]string{
	Messages:  {"text": "text", "image_url": "image", "file": "document"},
	Responses: {"text": "input_text", "image_url": "input_image", "file": "input_file", "refusal": "refusal"},
}

// Precomputed prefixes keep the existing writers' single append and allocation profile.
var (
	messagesTextHead     = `{"type":"` + contentNames[Messages]["text"] + `","text":`
	messagesImageHead    = `{"type":"` + contentNames[Messages]["image_url"] + `","source":`
	messagesFileHead     = `{"type":"` + contentNames[Messages]["file"] + `","source":{"type":"base64","media_type":"application/pdf","data":`
	responsesTextHead    = `{"type":"` + contentNames[Responses]["text"] + `","text":`
	responsesImageHead   = `{"type":"` + contentNames[Responses]["image_url"] + `","image_url":`
	responsesFileHead    = `{"type":"` + contentNames[Responses]["file"] + `","file_data":`
	responsesFileIDHead  = `{"type":"` + contentNames[Responses]["file"] + `","file_id":`
	responsesRefusalHead = `{"type":"` + contentNames[Responses]["refusal"] + `","refusal":`
)

func nativeContent(v jsontext.Value, f Format, role string, b *budget) (jsontext.Value, error) {
	if v.Kind() == '"' || isNull(v) {
		return v, nil
	}
	parts, err := arrayWithin(v, b)
	if err != nil {
		return nil, err
	}
	if err := b.draw(rounded(int64(len(parts)) * 128)); err != nil {
		return nil, err
	}
	out := make([]requestObject, 0, len(parts))
	for _, raw := range parts {
		o, err := membersWithin(raw, b)
		if err != nil {
			return nil, err
		}
		part, err := nativePart(o, string(o.take("type")), f, role, b)
		if err != nil {
			return nil, err
		}
		if err := o.remaining(); err != nil {
			return nil, err
		}
		out = append(out, part)
	}
	return jsonValueWithin(out, b), nil
}

func nativePart(o requestObject, quotedKind string, f Format, role string, b *budget) (requestObject, error) {
	chatKind, err := nativePartKind(o, quotedKind, f, role, b)
	if err != nil {
		return nil, err
	}
	part := requestObject{"type": jsonValueWithin(chatKind, b)}
	switch chatKind {
	case "text":
		part["text"] = o.take("text")
	case "refusal":
		part["refusal"] = o.take("refusal")
	case "image_url":
		if role != "user" {
			return nil, &NotSupported{Reason: "an image outside user content"}
		}
		url, err := nativeImage(o, f, b)
		if err != nil {
			return nil, err
		}
		part["image_url"] = jsonValueWithin(url, b)
	case "file":
		file, err := nativeFile(o, f, b)
		if err != nil {
			return nil, err
		}
		part["file"] = jsonValueWithin(file, b)
	default:
		return nil, &NotSupported{Reason: "content part has no Chat counterpart"}
	}
	o.move(part, "cache_control", "cache_control")
	if f == Responses {
		o.take("prompt_cache_breakpoint") // a cache hint carries no content; its TTL cannot be recovered
	}
	return part, nil
}

func nativePartKind(o requestObject, quotedKind string, f Format, role string, b *budget) (string, error) {
	kind, err := requestString(jsontext.Value(quotedKind), b)
	if err != nil {
		return "", err
	}
	chatKind := inverseName(contentNames[f], kind)
	if f == Responses && kind == "output_text" && role == "assistant" {
		chatKind = "text"
		if annotations := o.take("annotations"); !isNull(annotations) && string(annotations) != "[]" {
			return "", &NotSupported{Reason: "annotations have no Chat counterpart"}
		}
		// A replayed output item carries the logprobs the supplier returned; an empty list says nothing and is
		// dropped, while values have no Messages counterpart.
		if logprobs := o.take("logprobs"); !isNull(logprobs) && string(logprobs) != "[]" {
			return "", &NotSupported{Reason: "logprobs have no Chat counterpart"}
		}
	}
	if role == "tool" && chatKind != "text" {
		return "", &NotSupported{Reason: "Chat tool results accept only text"}
	}
	return chatKind, nil
}

func nativeFile(o requestObject, f Format, b *budget) (requestObject, error) {
	file := requestObject{}
	if f == Responses {
		if err := fileDetail(o); err != nil {
			return nil, err
		}
		o.move(file, "file_data", "file_data")
		o.move(file, "file_id", "file_id")
		o.move(file, "filename", "filename")
		switch {
		case isNull(file["file_data"]) && isNull(file["file_id"]):
			return nil, &NotSupported{Reason: "a file without data or an id cannot be replayed"}
		case !isNull(file["file_data"]) && !isNull(file["file_id"]):
			return nil, &NotSupported{Reason: "a file carrying both data and an id"}
		}
		return file, nil
	}
	source, err := membersWithin(o.take("source"), b)
	if err != nil {
		return nil, err
	}
	if string(source.take("type")) != `"base64"` || string(source.take("media_type")) != `"application/pdf"` {
		return nil, &NotSupported{Reason: "only inline PDF documents convert to Chat"}
	}
	data, err := requestString(source.take("data"), b)
	if err != nil {
		return nil, err
	}
	if err := b.draw(rounded(int64(len(data) + 32))); err != nil {
		return nil, err
	}
	file["file_data"] = jsonValueWithin("data:application/pdf;base64,"+data, b)
	// Anthropic's title is the document's name, which Chat calls the file's name.
	o.move(file, "title", "filename")
	return file, source.remaining()
}

// fileDetail consumes a Responses PDF's detail, which the Chat file has no member for: auto, the default, lets the
// supplier choose, while low and high ask for a specific page-image size that the conversion cannot select.
func fileDetail(o requestObject) error {
	switch string(o.take("detail")) {
	case "", "null", `"auto"`:
		return nil
	case `"low"`:
		return &NotSupported{Reason: "file detail low is not converted"}
	case `"high"`:
		return &NotSupported{Reason: "file detail high is not converted"}
	default:
		return shapeError("a file detail that is not auto, low or high")
	}
}

func nativeImage(o requestObject, f Format, b *budget) (requestObject, error) {
	image := requestObject{}
	if f == Responses {
		o.move(image, "image_url", "url")
		o.move(image, "detail", "detail")
		if _, err := requestString(image["url"], b); err != nil {
			return nil, err
		}
		return image, nil
	}
	source, err := membersWithin(o.take("source"), b)
	if err != nil {
		return nil, err
	}
	switch string(source.take("type")) {
	case `"url"`:
		source.move(image, "url", "url")
		if _, err := requestString(image["url"], b); err != nil {
			return nil, err
		}
	case `"base64"`:
		media, err := requestString(source.take("media_type"), b)
		if err != nil {
			return nil, err
		}
		data, err := requestString(source.take("data"), b)
		if err != nil {
			return nil, err
		}
		if err := b.draw(rounded(int64(len(media) + len(data) + 32))); err != nil {
			return nil, err
		}
		image["url"] = jsonValueWithin("data:"+media+";base64,"+data, b)
	default:
		return nil, &NotSupported{Reason: "image source has no Chat counterpart"}
	}
	return image, source.remaining()
}
