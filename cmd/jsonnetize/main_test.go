package main

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestJsonnetizer_QualifyOutput(t *testing.T) {
	j := Jsonnetizer{
		Base:   "/abc/123",
		Output: "/output/here",
	}

	assert.Equal(t, "/output/here/abc/123/xyz/my.resource", j.QualifyOutput("/abc/123/xyz", "my.resource"))
}
