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

func nativeContent(v jsontext.Value, f Format, role string) (jsontext.Value, error) {
	if v.Kind() == '"' || isNull(v) {
		return v, nil
	}
	parts, err := requestArray(v)
	if err != nil {
		return nil, err
	}
	out := make([]requestObject, 0, len(parts))
	for _, raw := range parts {
		o, err := requestMembers(raw)
		if err != nil {
			return nil, err
		}
		part, err := nativePart(o, string(o.take("type")), f, role)
		if err != nil {
			return nil, err
		}
		if err := o.remaining(); err != nil {
			return nil, err
		}
		out = append(out, part)
	}
	return jsonValue(out), nil
}

func nativePart(o requestObject, quotedKind string, f Format, role string) (requestObject, error) {
	chatKind, err := nativePartKind(o, quotedKind, f, role)
	if err != nil {
		return nil, err
	}
	part := requestObject{"type": jsonValue(chatKind)}
	switch chatKind {
	case "text":
		part["text"] = o.take("text")
	case "refusal":
		part["refusal"] = o.take("refusal")
	case "image_url":
		if role != "user" {
			return nil, &NotSupported{Reason: "an image outside user content"}
		}
		url, err := nativeImage(o, f)
		if err != nil {
			return nil, err
		}
		part["image_url"] = jsonValue(url)
	case "file":
		file, err := nativeFile(o, f)
		if err != nil {
			return nil, err
		}
		part["file"] = jsonValue(file)
	default:
		return nil, &NotSupported{Reason: "content part has no Chat counterpart"}
	}
	o.move(part, "cache_control", "cache_control")
	if f == Responses {
		o.take("prompt_cache_breakpoint") // a cache hint carries no content; its TTL cannot be recovered
	}
	return part, nil
}

func nativePartKind(o requestObject, quotedKind string, f Format, role string) (string, error) {
	kind, err := requestString(jsontext.Value(quotedKind))
	if err != nil {
		return "", err
	}
	chatKind := inverseName(contentNames[f], kind)
	if f == Responses && kind == "output_text" && role == "assistant" {
		chatKind = "text"
		if annotations := o.take("annotations"); !isNull(annotations) && string(annotations) != "[]" {
			return "", &NotSupported{Reason: "annotations have no Chat counterpart"}
		}
	}
	if role == "tool" && chatKind != "text" {
		return "", &NotSupported{Reason: "Chat tool results accept only text"}
	}
	return chatKind, nil
}

func nativeFile(o requestObject, f Format) (requestObject, error) {
	file := requestObject{}
	if f == Responses {
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
	source, err := requestMembers(o.take("source"))
	if err != nil {
		return nil, err
	}
	if string(source.take("type")) != `"base64"` || string(source.take("media_type")) != `"application/pdf"` {
		return nil, &NotSupported{Reason: "only inline PDF documents convert to Chat"}
	}
	data, err := requestString(source.take("data"))
	if err != nil {
		return nil, err
	}
	file["file_data"] = jsonValue("data:application/pdf;base64," + data)
	// Anthropic's title is the document's name, which Chat calls the file's name.
	o.move(file, "title", "filename")
	return file, source.remaining()
}

func nativeImage(o requestObject, f Format) (requestObject, error) {
	image := requestObject{}
	if f == Responses {
		o.move(image, "image_url", "url")
		o.move(image, "detail", "detail")
		if _, err := requestString(image["url"]); err != nil {
			return nil, err
		}
		return image, nil
	}
	source, err := requestMembers(o.take("source"))
	if err != nil {
		return nil, err
	}
	switch string(source.take("type")) {
	case `"url"`:
		source.move(image, "url", "url")
		if _, err := requestString(image["url"]); err != nil {
			return nil, err
		}
	case `"base64"`:
		media, err := requestString(source.take("media_type"))
		if err != nil {
			return nil, err
		}
		data, err := requestString(source.take("data"))
		if err != nil {
			return nil, err
		}
		image["url"] = jsonValue("data:" + media + ";base64," + data)
	default:
		return nil, &NotSupported{Reason: "image source has no Chat counterpart"}
	}
	return image, source.remaining()
}
