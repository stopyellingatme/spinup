package digitalocean

import "testing"

func TestAPIErrorTryForInvalidDropletChoice(t *testing.T) {
	t.Parallel()

	for _, message := range []string{
		"size is not available in this region",
		"the selected region is invalid",
		"image is unavailable",
	} {
		err := &APIError{Status: 422, Message: message}
		if got, want := err.Try(), "run spinup deploy digital-ocean --plan to list available sizes, or pass --size/--region/--image"; got != "Try: "+want {
			t.Errorf("Try() = %q, want %q", got, "Try: "+want)
		}
	}
}
