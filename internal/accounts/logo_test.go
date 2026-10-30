package accounts_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"axiom/internal/accounts"
)

func TestParseLogoSVGSanitisesToAnAllowlist(t *testing.T) {
	t.Parallel()
	upload := []byte(`<?xml version="1.0"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"
     width="24" height="24" viewBox="0 0 24 24" onload="alert(1)">
  <!-- a design tool's metadata -->
  <title>A &amp; B</title>
  <script>alert(1)</script>
  <style>@import url(https://evil.example/x.css);</style>
  <foreignObject><div xmlns="http://www.w3.org/1999/xhtml"><img src="x" onerror="alert(1)"/></div></foreignObject>
  <image href="https://evil.example/x.png"/>
  <a xlink:href="javascript:alert(1)"><text>go</text></a>
  <defs><linearGradient id="g"><stop offset="0" stop-color="#fff"/></linearGradient></defs>
  <use xlink:href="#g" href="#g"/>
  <path style="fill:url(https://evil.example/x)" onclick="alert(1)" fill="url(https://evil.example/x)"
        stroke="url(#g)" d="M0 0h24v24H0z"/>
</svg>`)
	logo, err := accounts.ParseLogo(accounts.LogoSVG, upload)
	if err != nil {
		t.Fatalf("ParseLogo: %v", err)
	}
	if logo.ContentType != accounts.LogoSVG || logo.Width != 24 || logo.Height != 24 {
		t.Fatalf("logo = %s %dx%d", logo.ContentType, logo.Width, logo.Height)
	}
	out := string(logo.Bytes)
	checkAbsent(t, out, "script", "onload", "onclick", "style=", "foreignObject", "<image", "javascript", "evil.example", "<!--")
	checkPresent(t, out, `xmlns="http://www.w3.org/2000/svg"`, `xlink:href="#g"`, `stroke="url(#g)"`, "A &amp; B", "<path")
	if logo.HashHex() == "" || len(logo.Hash) != 32 {
		t.Errorf("hash = %x", logo.Hash)
	}
	again, err := accounts.ParseLogo(accounts.LogoSVG, upload)
	if err != nil || !bytes.Equal(again.Bytes, logo.Bytes) || again.Hash != logo.Hash {
		t.Errorf("the same SVG sanitised differently: %v", err)
	}
}

func checkAbsent(t *testing.T, out string, words ...string) {
	t.Helper()
	for _, word := range words {
		if strings.Contains(out, word) {
			t.Errorf("sanitised SVG still has %q:\n%s", word, out)
		}
	}
}

func checkPresent(t *testing.T, out string, words ...string) {
	t.Helper()
	for _, word := range words {
		if !strings.Contains(out, word) {
			t.Errorf("sanitised SVG lost %q:\n%s", word, out)
		}
	}
}

func TestParseLogoSVGRejectsWhatIsNotOnePicture(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a document type":  `<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg"><text>&x;</text></svg>`,
		"broken XML":       `<svg xmlns="http://www.w3.org/2000/svg"><path></svg>`,
		"no svg root":      `<html xmlns="http://www.w3.org/1999/xhtml"></html>`,
		"too large a side": `<svg xmlns="http://www.w3.org/2000/svg" width="2048" height="24"></svg>`,
		"a huge viewBox":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4096 4096"></svg>`,
		"two roots":        `<svg xmlns="http://www.w3.org/2000/svg"></svg><svg xmlns="http://www.w3.org/2000/svg"></svg>`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := accounts.ParseLogo(accounts.LogoSVG, []byte(source)); err == nil {
				t.Fatal("ParseLogo accepted it")
			}
		})
	}
}

func TestParseLogoSVGUsesTheViewBoxWhenThereIsNoPixelSize(t *testing.T) {
	t.Parallel()
	logo, err := accounts.ParseLogo(accounts.LogoSVG, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 32"></svg>`))
	if err != nil {
		t.Fatalf("ParseLogo: %v", err)
	}
	if logo.Width != 48 || logo.Height != 32 {
		t.Fatalf("logo = %dx%d", logo.Width, logo.Height)
	}
	logo, err = accounts.ParseLogo(accounts.LogoSVG, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	if err != nil || logo.Width != 0 || logo.Height != 0 {
		t.Fatalf("a scalable logo = %dx%d, %v", logo.Width, logo.Height, err)
	}
}

func TestParseLogoRasterImages(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	logo, err := accounts.ParseLogo(accounts.LogoPNG, encoded.Bytes())
	if err != nil {
		t.Fatalf("ParseLogo PNG: %v", err)
	}
	if logo.Width != 3 || logo.Height != 2 || !bytes.Equal(logo.Bytes, encoded.Bytes()) {
		t.Fatalf("PNG logo = %dx%d, %d bytes", logo.Width, logo.Height, len(logo.Bytes))
	}
	if _, err := accounts.ParseLogo(accounts.LogoWebP, encoded.Bytes()); err == nil {
		t.Fatal("PNG bytes were accepted as WebP")
	}
	if _, err := accounts.ParseLogo(accounts.LogoPNG, []byte("not a png")); err == nil {
		t.Fatal("garbage was accepted as PNG")
	}

	webp := make([]byte, 30)
	copy(webp[0:], "RIFF")
	copy(webp[8:], "WEBP")
	copy(webp[12:], "VP8X")
	webp[16] = 10                                   // chunk size, little-endian
	webp[24], webp[25], webp[26] = 0x1f, 0x03, 0x00 // 800 - 1
	webp[27], webp[28], webp[29] = 0x2f, 0x01, 0x00 // 304 - 1
	logo, err = accounts.ParseLogo(accounts.LogoWebP, webp)
	if err != nil {
		t.Fatalf("ParseLogo WebP: %v", err)
	}
	if logo.Width != 800 || logo.Height != 304 {
		t.Fatalf("WebP logo = %dx%d", logo.Width, logo.Height)
	}
}

func TestParseLogoBoundsTheFileAndItsSides(t *testing.T) {
	t.Parallel()
	if _, err := accounts.ParseLogo(accounts.LogoSVG, make([]byte, (256<<10)+1)); err == nil {
		t.Fatal("a file over 256 KiB was accepted")
	}
	tooWide := image.NewRGBA(image.Rect(0, 0, 1025, 1))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, tooWide); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.ParseLogo(accounts.LogoPNG, encoded.Bytes()); err == nil {
		t.Fatal("an image over 1024 pixels wide was accepted")
	}
	if _, err := accounts.ParseLogo("image/gif", []byte("GIF89a")); err == nil {
		t.Fatal("an unsupported content type was accepted")
	}
	if _, err := accounts.ParseLogo(accounts.LogoPNG, nil); err == nil {
		t.Fatal("an empty file was accepted")
	}
}
