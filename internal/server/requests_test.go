package server

import (
	"testing"
)

func TestFontRequests(t *testing.T) {
	_, ts, admin := newTestServer(t)
	admin.do("POST", "/api/users", map[string]any{"username": "eva", "password": "evapassword1"}, nil)
	admin.do("POST", "/api/users", map[string]any{"username": "finn", "password": "finnpassword1"}, nil)
	login := func(n string) *client {
		c := newClient(t, ts.URL)
		c.do("POST", "/api/auth/login", map[string]string{"username": n, "password": n + "password1"}, nil)
		return c
	}
	eva, finn := login("eva"), login("finn")

	var created map[string]int64
	if st := eva.do("POST", "/api/requests", map[string]any{"font": "Brandon Grotesque", "purpose": "flyer for the spring campaign"}, &created); st != 201 {
		t.Fatalf("create: %d", st)
	}
	if st := eva.do("POST", "/api/requests", map[string]any{"font": "brandon grotesque"}, nil); st != 409 {
		t.Fatalf("duplicate open request: %d", st)
	}
	if st := eva.do("POST", "/api/requests", map[string]any{"font": "  "}, nil); st != 400 {
		t.Fatalf("empty name: %d", st)
	}
	finn.do("POST", "/api/requests", map[string]any{"font": "Inter"}, nil)

	// each user sees their own, admins see all
	var mine, all []fontRequest
	eva.do("GET", "/api/requests", nil, &mine)
	admin.do("GET", "/api/requests", nil, &all)
	if len(mine) != 1 || mine[0].Font != "Brandon Grotesque" || len(all) != 2 {
		t.Fatalf("lists: mine=%+v all=%d", mine, len(all))
	}

	// only admins answer; the answer is unseen until eva looks
	id := itoa(created["id"])
	if st := finn.do("PATCH", "/api/requests/"+id, map[string]any{"status": "done"}, nil); st != 403 {
		t.Fatalf("non-admin answering: %d", st)
	}
	if st := admin.do("PATCH", "/api/requests/"+id, map[string]any{"status": "done", "answer": "Added to the Brand collection"}, nil); st != 200 {
		t.Fatalf("answer: %d", st)
	}
	eva.do("GET", "/api/requests", nil, &mine)
	if mine[0].Status != "done" || mine[0].Seen || mine[0].Answer != "Added to the Brand collection" || mine[0].ResolvedBy == "" {
		t.Fatalf("answered request: %+v", mine[0])
	}
	eva.do("PATCH", "/api/requests/"+id, map[string]any{"seen": true}, nil)
	eva.do("GET", "/api/requests", nil, &mine)
	if !mine[0].Seen {
		t.Fatal("seen not stored")
	}

	// withdraw: own open requests only
	var finnReqs []fontRequest
	finn.do("GET", "/api/requests", nil, &finnReqs)
	if st := eva.do("DELETE", "/api/requests/"+itoa(finnReqs[0].ID), nil, nil); st != 403 {
		t.Fatalf("withdrawing someone else's request: %d", st)
	}
	if st := eva.do("DELETE", "/api/requests/"+id, nil, nil); st != 403 {
		t.Fatalf("withdrawing an answered request: %d", st)
	}
	if st := finn.do("DELETE", "/api/requests/"+itoa(finnReqs[0].ID), nil, nil); st != 200 {
		t.Fatalf("withdraw own: %d", st)
	}
}
