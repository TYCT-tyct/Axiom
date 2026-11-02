package fileparse

import (
	"bytes"
	"fmt"
	"strconv"
)

// looksPDF finds the PDF header in the first KiB, where the format allows leading bytes before it.
func looksPDF(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("%PDF-"))
}

// physicalPages reads Docling's page dictionary. It requires the numbered pages to be complete: a dictionary that
// only contains pages with content would otherwise undercount a blank page, which is exactly the billing error this
// count exists to avoid.
func physicalPages(pages map[string]struct{}) (int, error) {
	if len(pages) == 0 {
		return 0, fmt.Errorf("%w: the backend did not report the PDF's pages", ErrUpstream)
	}
	for page := 1; page <= len(pages); page++ {
		if _, ok := pages[strconv.Itoa(page)]; !ok {
			return 0, fmt.Errorf("%w: the PDF page dictionary is missing page %d", ErrUpstream, page)
		}
	}
	return len(pages), nil
}
