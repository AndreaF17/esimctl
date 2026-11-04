// Package qr extracts eSIM activation codes from QR codes on screen or in the
// clipboard, using grim, slurp, wl-paste and zbarimg.
package qr

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

// FromScreen lets the user select a screen region and decodes the QR in it.
func FromScreen() (string, error) {
	region, err := exec.Command("slurp").Output()
	if err != nil {
		return "", errors.New("selection cancelled")
	}
	img, err := exec.Command("grim", "-g", strings.TrimSpace(string(region)), "-").Output()
	if err != nil {
		return "", errors.New("screenshot failed (is grim installed?)")
	}
	return decode(img)
}

// FromClipboard returns an activation code copied as text, or decodes a
// copied QR image.
func FromClipboard() (string, error) {
	if txt, err := exec.Command("wl-paste", "-n", "-t", "text/plain").Output(); err == nil {
		if code := find(string(txt)); code != "" {
			return code, nil
		}
	}
	img, err := exec.Command("wl-paste", "-t", "image/png").Output()
	if err != nil || len(img) == 0 {
		return "", errors.New("no activation code or QR image in the clipboard")
	}
	return decode(img)
}

func decode(img []byte) (string, error) {
	cmd := exec.Command("zbarimg", "-q", "--raw", "-")
	cmd.Stdin = bytes.NewReader(img)
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("no QR code found")
	}
	if code := find(string(out)); code != "" {
		return code, nil
	}
	return "", errors.New("the QR code is not an eSIM activation code")
}

func find(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(strings.ToUpper(line), "LPA:1$"); i >= 0 {
			return "LPA:" + line[i+4:]
		}
	}
	return ""
}
