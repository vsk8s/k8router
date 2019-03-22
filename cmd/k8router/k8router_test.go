package main

import (
	"reflect"
	"testing"
)

func TestSliceDifference(t *testing.T) {
	testCases := []struct {
		s        []string
		toRemove []string
		result   []string
	}{
		{
			[]string{"this", "is", "a", "test"},
			[]string{},
			[]string{"this", "is", "a", "test"},
		},
		{[]string{"a", "b"},
			[]string{"a"},
			[]string{"b"},
		},
		{
			[]string{"a", "a", "a", "b"},
			[]string{"a"},
			[]string{"a", "a", "b"},
		},
		{
			[]string{},
			[]string{"a", "b"},
			[]string{},
		},
		{
			[]string{"a", "a", "b", "b", "b"},
			[]string{"a", "b", "b"},
			[]string{"a", "b"},
		},
	}

	for _, c := range testCases {
		res := sliceDifference(c.s, c.toRemove)
		if !reflect.DeepEqual(res, c.result) {
			t.Logf("got %+v, expected %+v", res, c.result)
			t.Fail()
		}
	}
}
