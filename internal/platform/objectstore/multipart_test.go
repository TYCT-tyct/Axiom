package objectstore

import (
	"errors"
	"strings"
	"testing"
)

// multipartClient makes a ten-byte object three parts: the threshold is eight bytes and a part four, so the test does
// not move sixteen mebibytes through the double.
func multipartClient(t *testing.T, fake *fakeS3) *Client {
	t.Helper()
	client := newTestClient(t, fake)
	client.threshold = 8
	client.partSize = 4
	return client
}

func TestPutAboveTheThresholdUsesParts(t *testing.T) {
	fake := newFakeS3()
	client := multipartClient(t, fake)
	info, err := client.Put(deadline(t), Upload{Key: "archive.bin", Body: strings.NewReader("abcdefghij"), Size: 10})
	if err != nil || info.Size != 10 || info.ETag != `"complete"` {
		t.Fatalf("Put = %+v, %v", info, err)
	}
	if got := fake.body("archive.bin"); got != "abcdefghij" {
		t.Fatalf("stored %q, want abcdefghij", got)
	}
	if fake.uploadCount() != 0 {
		t.Fatalf("the upload survived completion")
	}
	paths := strings.Join(fake.seen(), "\n")
	for _, want := range []string{"?uploads=", "partNumber=1&uploadId=upload-1", "partNumber=3&uploadId=upload-1", "uploadId=upload-1"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("requests lack %q:\n%s", want, paths)
		}
	}
}

func TestPutAtTheThresholdStaysOneRequest(t *testing.T) {
	fake := newFakeS3()
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefgh"), Size: 8}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if strings.Contains(strings.Join(fake.seen(), "\n"), "uploads") {
		t.Fatalf("a body at the threshold started a multipart upload: %v", fake.seen())
	}
}

func TestMultipartAbortsWhenAPartKeepsFailing(t *testing.T) {
	fake := newFakeS3()
	fake.failPartAt = 2
	client := multipartClient(t, fake)
	_, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10})
	if err == nil {
		t.Fatal("a failing part was accepted")
	}
	if got := fake.aborts(); len(got) != 1 || got[0] != "upload-1" {
		t.Fatalf("aborted = %v, want upload-1", got)
	}
	if fake.uploadCount() != 0 {
		t.Fatalf("the failed upload survived")
	}
	partAttempts := 0
	for _, path := range fake.seen() {
		if strings.Contains(path, "partNumber=2") {
			partAttempts++
		}
	}
	if partAttempts != maxAttempts {
		t.Fatalf("part 2 was attempted %d times, want %d", partAttempts, maxAttempts)
	}
}

func TestMultipartAbortsWhenCompletionFails(t *testing.T) {
	fake := newFakeS3()
	fake.failDone = true
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10}); err == nil {
		t.Fatal("a failing completion was accepted")
	}
	if got := fake.aborts(); len(got) != 1 {
		t.Fatalf("aborted = %v, want one abort", got)
	}
}

func TestMultipartRejectsATwoHundredErrorDocument(t *testing.T) {
	fake := newFakeS3()
	fake.errorDoneLeft = 10 // more than the client attempts
	client := multipartClient(t, fake)
	_, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10})
	var remote *Error
	if !errors.As(err, &remote) || remote.Code != "InternalError" {
		t.Fatalf("err = %v, want the 200 Error document", err)
	}
	if got := fake.aborts(); len(got) != 1 {
		t.Fatalf("aborted = %v, want one abort", got)
	}
}

func TestMultipartRetriesA200ErrorDocument(t *testing.T) {
	fake := newFakeS3()
	fake.errorDoneLeft = 1
	client := multipartClient(t, fake)
	info, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if info.ETag != `"complete"` || fake.body("k") != "abcdefghij" {
		t.Fatalf("completed %+v with %q", info, fake.body("k"))
	}
	if fake.abortCount() != 0 {
		t.Fatalf("aborted a retry that succeeded")
	}
}

func TestMultipartRejectsACompletionWithoutAnETag(t *testing.T) {
	fake := newFakeS3()
	fake.noETag = true
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10}); err == nil {
		t.Fatal("a completion without an ETag was accepted")
	}
	if fake.abortCount() == 0 {
		t.Fatal("the failed upload was not aborted")
	}
}

func TestMultipartRefusesASizeThatDoesNotMatchBeforeUploading(t *testing.T) {
	fake := newFakeS3()
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 9}); err == nil {
		t.Fatal("a wrong Size was accepted")
	}
	if len(fake.seen()) != 0 {
		t.Fatalf("a mismatch reached the server: %v", fake.seen())
	}
}
