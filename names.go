package main

import "math/rand/v2"

// Docker-style filler names, used when a caller omits a title. Deliberately
// bland and friendly: a title is a label in a list, not a description, and an
// untitled entry reading "quiet-harbor" beats one reading "Untitled".
var (
	adjectives = []string{
		"amber", "brisk", "calm", "clever", "cosmic", "crisp", "dapper", "dusty",
		"eager", "fabled", "faint", "fleet", "gentle", "gilded", "hidden", "hollow",
		"humble", "idle", "jolly", "keen", "lucid", "mellow", "misty", "muted",
		"nimble", "noble", "patient", "placid", "polite", "quiet", "rapid", "rustic",
		"scarlet", "silent", "silken", "solemn", "spry", "still", "sunny", "swift",
		"tidy", "timid", "tranquil", "velvet", "vivid", "wandering", "weathered",
		"winding", "wistful", "zesty",
	}
	nouns = []string{
		"anchor", "arbor", "atlas", "beacon", "bramble", "canyon", "cedar", "cinder",
		"cobble", "compass", "cove", "current", "dawn", "delta", "dune", "ember",
		"fathom", "ferry", "fjord", "glade", "harbor", "hearth", "heron", "hollow",
		"lantern", "ledger", "meadow", "meridian", "moor", "orchard", "otter",
		"pebble", "quarry", "quill", "ridge", "rill", "sable", "shoal", "sparrow",
		"summit", "thicket", "thistle", "tide", "timber", "vale", "wharf", "willow",
		"windlass", "yonder", "zephyr",
	}
)

func generatedName() string {
	return adjectives[rand.IntN(len(adjectives))] + "-" + nouns[rand.IntN(len(nouns))]
}
