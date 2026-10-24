package main

import "encoding/json"

func writeJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(name, append(data, '\n'))
}
