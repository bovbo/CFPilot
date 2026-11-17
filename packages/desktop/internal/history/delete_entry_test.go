package history

import "testing"

func TestDeleteEntry(t *testing.T) {
	st := New(t.TempDir(), 10)
	if err := st.Save(Session{ID: "a", Host: "h", Entries: []Entry{{IP: "1.1.1.1", Port: 443}, {IP: "2.2.2.2", Port: 443}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteEntry("a", "1.1.1.1", 443); err != nil {
		t.Fatal(err)
	}
	l, _ := st.Load()
	if len(l) != 1 || len(l[0].Entries) != 1 || l[0].Entries[0].IP != "2.2.2.2" {
		t.Fatalf("got %+v", l)
	}
	_ = st.DeleteEntry("a", "2.2.2.2", 443)
	l, _ = st.Load()
	if len(l) != 0 {
		t.Fatalf("空会话应被删除：%+v", l)
	}
}
