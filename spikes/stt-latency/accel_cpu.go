//go:build whispercpp && !wcuda && !wvulkan

package main

// compiledAccel is the backend the linked libwhisper was built with.
const compiledAccel = "cpu"
