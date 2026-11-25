// Package dictation holds dictation behaviour: the simple-tier cleanup
// (filler words, repeated words), mid-dictation commands such as "new
// paragraph" and "scratch that", and the choice between typing and pasting
// at the cursor (ADR-0022). Phrase lists are tables, so a new filler or
// command is one more row; the smart tier will sit behind the same Cleaner.
package dictation
