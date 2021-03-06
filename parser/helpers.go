package parser

import (
	"github.com/Jeffail/gabs/v2"
	"github.com/buger/jsonparser"
	"github.com/iancoleman/strcase"
	"github.com/pkg/errors"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Regex to match anything that has a value matching the format of {{ config.$1 }} which
// will cause the program to lookup that configuration value from itself and set that
// value to the configuration one.
//
// This allows configurations to reference values that are node dependent, such as the
// internal IP address used by the daemon, useful in Bungeecord setups for example, where
// it is common to see variables such as "{{config.docker.interface}}"
var configMatchRegex = regexp.MustCompile(`{{\s?config\.([\w.-]+)\s?}}`)

// Regex to support modifying XML inline variable data using the config tools. This means
// you can pass a replacement of Root.Property='[value="testing"]' to get an XML node
// matching:
//
// <Root>
//   <Property value="testing"/>
// </Root>
//
// noinspection RegExpRedundantEscape
var xmlValueMatchRegex = regexp.MustCompile(`^\[([\w]+)='(.*)'\]$`)

// Gets the []byte representation of a configuration file to be passed through to other
// handler functions. If the file does not currently exist, it will be created.
func readFileBytes(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return ioutil.ReadAll(file)
}

// Helper function to set the value of the JSON key item based on the jsonparser value
// type returned.
func setPathway(c *gabs.Container, path string, value []byte, vt jsonparser.ValueType) error {
	v := getKeyValue(value, vt)

	_, err := c.SetP(v, path)

	return err
}

// Gets the value of a key based on the value type defined.
func getKeyValue(value []byte, vt jsonparser.ValueType) interface{} {
	switch vt {
	case jsonparser.Number:
		{
			v, _ := strconv.Atoi(string(value))
			return v
		}
	case jsonparser.Boolean:
		{
			v, _ := strconv.ParseBool(string(value))
			return v
		}
	default:
		return string(value)
	}
}

// Iterate over an unstructured JSON/YAML/etc. interface and set all of the required
// key/value pairs for the configuration file.
//
// We need to support wildcard characters in key searches, this allows you to make
// modifications to multiple keys at once, especially useful for games with multiple
// configurations per-world (such as Spigot and Bungeecord) where we'll need to make
// adjustments to the bind address for the user.
//
// This does not currently support nested matches. container.*.foo.*.bar will not work.
func (f *ConfigurationFile) IterateOverJson(data []byte) (*gabs.Container, error) {
	parsed, err := gabs.ParseJSON(data)
	if err != nil {
		return nil, err
	}

	for _, v := range f.Replace {
		value, dt, err := f.LookupConfigurationValue(v)
		if err != nil {
			return nil, err
		}

		// Check for a wildcard character, and if found split the key on that value to
		// begin doing a search and replace in the data.
		if strings.Contains(v.Match, ".*") {
			parts := strings.SplitN(v.Match, ".*", 2)

			// Iterate over each matched child and set the remaining path to the value
			// that is passed through in the loop.
			//
			// If the child is a null value, nothing will happen. Seems reasonable as of the
			// time this code is being written.
			for _, child := range parsed.Path(strings.Trim(parts[0], ".")).Children() {
				if err := setPathway(child, strings.Trim(parts[1], "."), value, dt); err != nil {
					return nil, err
				}
			}
		} else {
			if err = setPathway(parsed, v.Match, value, dt); err != nil {
				return nil, err
			}
		}
	}

	return parsed, nil
}

// Looks up a configuration value on the Daemon given a dot-notated syntax.
func (f *ConfigurationFile) LookupConfigurationValue(cfr ConfigurationFileReplacement) ([]byte, jsonparser.ValueType, error) {
	if !configMatchRegex.Match([]byte(cfr.Value)) {
		return []byte(cfr.Value), cfr.ValueType, nil
	}

	// If there is a match, lookup the value in the configuration for the Daemon. If no key
	// is found, just return the string representation, otherwise use the value from the
	// daemon configuration here.
	huntPath := configMatchRegex.ReplaceAllString(
		configMatchRegex.FindString(cfr.Value), "$1",
	)

	var path []string
	// The camel casing is important here, the configuration for the Daemon does not use
	// JSON, and as such all of the keys will be generated in CamelCase format, rather than
	// the expected snake_case from the old Daemon.
	for _, value := range strings.Split(huntPath, ".") {
		path = append(path, strcase.ToCamel(value))
	}

	// Look for the key in the configuration file, and if found return that value to the
	// calling function.
	match, dt, _, err := jsonparser.Get(f.configuration, path...)
	if err != nil {
		if err != jsonparser.KeyPathNotFoundError {
			return match, dt, errors.WithStack(err)
		}

		// If there is no key, keep the original value intact, that way it is obvious there
		// is a replace issue at play.
		return []byte(cfr.Value), cfr.ValueType, nil
	} else {
		replaced := []byte(configMatchRegex.ReplaceAllString(cfr.Value, string(match)))

		return replaced, cfr.ValueType, nil
	}
}
