// Package persona loads and validates persona packs (design §27, ADR-0036):
// the folder that gives Footman a name, a voice, a manner of speaking and a
// set of stock lines. It also finds packs (shipped ones embedded in the
// binary, user ones in <data>/personas) and installs and removes user packs.
//
// A pack is data, not code. It cannot declare tools, permissions or config
// outside its own fields, and its style text is untrusted: SystemPrompt
// confines it to a delimited section that the core rules follow and
// override. The clip bank (rendering and caching audio for the lines) lives in
// internal/persona/clipbank and is built on the line accessors here.
package persona
