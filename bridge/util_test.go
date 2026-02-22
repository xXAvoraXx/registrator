package bridge

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscapedComma(t *testing.T) {
	cases := []struct {
		Tag      string
		Expected []string
	}{
		{
			Tag:      "",
			Expected: []string{},
		},
		{
			Tag:      "foobar",
			Expected: []string{"foobar"},
		},
		{
			Tag:      "foo,bar",
			Expected: []string{"foo", "bar"},
		},
		{
			Tag:      "foo\\,bar",
			Expected: []string{"foo,bar"},
		},
		{
			Tag:      "foo,bar\\,baz",
			Expected: []string{"foo", "bar,baz"},
		},
		{
			Tag:      "\\,foobar\\,",
			Expected: []string{",foobar,"},
		},
		{
			Tag:      ",,,,foo,,,bar,,,",
			Expected: []string{"foo", "bar"},
		},
		{
			Tag:      ",,,,",
			Expected: []string{},
		},
		{
			Tag:      ",,\\,,",
			Expected: []string{","},
		},
	}

	for _, c := range cases {
		results := recParseEscapedComma(c.Tag)
		sort.Strings(c.Expected)
		sort.Strings(results)
		assert.EqualValues(t, c.Expected, results)
	}
}

func TestEnsureTag(t *testing.T) {
	tags := ensureTag([]string{"keygen", "db"}, registratorManagedTag)
	assert.EqualValues(t, []string{"keygen", "db", "registrator"}, tags)

	alreadyTagged := ensureTag([]string{"production", "Registrator"}, registratorManagedTag)
	assert.EqualValues(t, []string{"production", "Registrator"}, alreadyTagged)
}

func TestIsRegistratorManagedService(t *testing.T) {
	assert.True(t, isRegistratorManagedService(&Service{Tags: []string{"db", "registrator"}}))
	assert.True(t, isRegistratorManagedService(&Service{Tags: []string{"Registrator"}}))
	assert.False(t, isRegistratorManagedService(&Service{Tags: []string{"db"}}))
	assert.False(t, isRegistratorManagedService(nil))
}
