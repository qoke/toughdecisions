package store

import "testing"

func TestResponseCacheKey(t *testing.T) {
	a := ResponseCacheKey("judge", "ch", "pp", "ih", nil, 0)
	b := ResponseCacheKey("judge", "ch", "pp", "ih", nil, 0)
	if a == "" || a != b {
		t.Fatalf("same input gave different keys: %q vs %q", a, b)
	}
	// Empty and nil bundle hashes must agree: both mean "no bundle".
	empty := ResponseCacheKey("judge", "ch", "pp", "ih", strptr(""), 0)
	if empty != a {
		t.Fatalf("empty bundle hash key = %q, want %q", empty, a)
	}
	cases := map[string]string{
		"seat":       ResponseCacheKey("other", "ch", "pp", "ih", nil, 0),
		"config":     ResponseCacheKey("judge", "other", "pp", "ih", nil, 0),
		"pack":       ResponseCacheKey("judge", "ch", "other", "ih", nil, 0),
		"input":      ResponseCacheKey("judge", "ch", "pp", "other", nil, 0),
		"bundle":     ResponseCacheKey("judge", "ch", "pp", "ih", strptr("bh"), 0),
		"repetition": ResponseCacheKey("judge", "ch", "pp", "ih", nil, 1),
	}
	for field, key := range cases {
		if key == a {
			t.Fatalf("changing %s did not change the key", field)
		}
		if len(key) != 64 {
			t.Fatalf("%s key = %q, want 64 hex chars", field, key)
		}
	}
}

func TestMaxRepetition(t *testing.T) {
	db := openTestDB(t)
	max, err := db.MaxRepetition("judge", "ch", "pp", "ih", nil)
	if err != nil || max != -1 {
		t.Fatalf("empty MaxRepetition = %d, %v; want -1", max, err)
	}
	mk := func(rep int) *Response {
		return &Response{
			CacheKey: ResponseCacheKey("judge", "ch", "pp", "ih", nil, rep),
			Seat:     "judge", ConfigHash: "ch", PromptPackHash: "pp",
			InputHash: "ih", Repetition: rep, Origin: "harness",
			ModelRequested: "m",
		}
	}
	if _, err := db.InsertResponse(mk(0)); err != nil {
		t.Fatalf("insert rep 0: %v", err)
	}
	if _, err := db.InsertResponse(mk(1)); err != nil {
		t.Fatalf("insert rep 1: %v", err)
	}
	max, err = db.MaxRepetition("judge", "ch", "pp", "ih", nil)
	if err != nil || max != 1 {
		t.Fatalf("MaxRepetition = %d, %v; want 1", max, err)
	}
	if _, err := db.InsertResponse(&Response{
		CacheKey: ResponseCacheKey("judge", "ch", "pp", "ih", strptr("bh"), 0),
		Seat:     "judge", ConfigHash: "ch", PromptPackHash: "pp",
		InputHash: "ih", BundleHash: strptr("bh"), Origin: "harness",
		ModelRequested: "m",
	}); err != nil {
		t.Fatalf("insert bundled: %v", err)
	}
	bmax, err := db.MaxRepetition("judge", "ch", "pp", "ih", strptr("bh"))
	if err != nil || bmax != 0 {
		t.Fatalf("bundled MaxRepetition = %d, %v; want 0", bmax, err)
	}
}
