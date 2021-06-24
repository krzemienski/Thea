package main

import (
	"log"

	"gitlab.com/hbomb79/TPA/processor"
)

/**
 * Main function is the entry point to the program, from here will
 * we load the users TPA configuration from their home directory,
 * merging the configuration with the default config
 */
func main() {
	// Creates a new Processor struct, filling in the configuration
	t := processor.New()
	log.Printf("Test: %#v (%T)\n", t, t)

	// Start the program
	err := t.Begin()
	if err != nil {
		log.Fatalf("Failed to initialise Processer - %v\n", err.Error())
	}

	log.Printf("Test: %#v (%T)\n", t, t)
}
