package ci

import "testing"

func TestHeadCheckoutRefs(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{
		"${{ github.event.pull_request.head.sha }}",
		"${{ github.event.pull_request.merge_commit_sha }}",
		"refs/pull/${{ github.event.pull_request.number }}/merge",
		"refs/${{ github.event.number }}/merge",
		"${{ github.head_ref }}",
	} {
		if !headCheckout(ref) {
			t.Errorf("headCheckout(%q) = false, want true", ref)
		}
	}
	if headCheckout("${{ github.sha }}") {
		t.Error("headCheckout accepted the base commit as a head checkout")
	}
}
