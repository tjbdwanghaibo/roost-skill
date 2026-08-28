package skill

import "testing"

func TestDerivedEventPreservesRootAndSetsParent(t *testing.T) {
	root := newRootEvent(1)
	child := deriveEvent(root, 2)
	grandchild := deriveEvent(child, 3)
	if grandchild.RootEventID != root.EventID || grandchild.ParentEventID != child.EventID {
		t.Fatalf("broken causal identity: %#v", grandchild)
	}
}

func TestEventContextCopiesSortsAndDeduplicatesTags(t *testing.T) {
	tags := []GameplayTagHandle{3, 1, 3, 2}
	event := newRootEvent(1).WithGameplayTags(tags)
	tags[0] = 99
	want := []GameplayTagHandle{1, 2, 3}
	got := event.GameplayTags()
	if len(got) != len(want) {
		t.Fatalf("tags = %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("tags = %v", got)
		}
	}
	got[0] = 99
	if event.GameplayTags()[0] != 1 {
		t.Fatal("event exposed mutable tag storage")
	}
}
