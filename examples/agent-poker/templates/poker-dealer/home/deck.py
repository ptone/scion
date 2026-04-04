#!/usr/bin/env python3
"""
Deck management script for the poker dealer.

Maintains a shuffled deck of cards with persistent state between invocations.
State is stored in ~/.deck_state.json.

Usage:
    python3 deck.py init          # Create and shuffle a new 52-card deck
    python3 deck.py draw [N]      # Draw N cards (default: 1)
    python3 deck.py remaining     # Show how many cards remain
"""

import json
import os
import random
import sys

STATE_FILE = os.path.expanduser("~/.deck_state.json")

SUITS = ["hearts", "diamonds", "clubs", "spades"]
RANKS = ["2", "3", "4", "5", "6", "7", "8", "9", "10", "Jack", "Queen", "King", "Ace"]


def build_deck():
    return [f"{rank} of {suit}" for suit in SUITS for rank in RANKS]


def save_state(cards):
    with open(STATE_FILE, "w") as f:
        json.dump({"remaining": cards}, f, indent=2)


def load_state():
    if not os.path.exists(STATE_FILE):
        print("Error: No deck initialized. Run 'python3 deck.py init' first.", file=sys.stderr)
        sys.exit(1)
    with open(STATE_FILE) as f:
        return json.load(f)["remaining"]


def cmd_init():
    deck = build_deck()
    random.shuffle(deck)
    save_state(deck)
    print(f"Deck initialized and shuffled. {len(deck)} cards ready.")


def cmd_draw(n=1):
    cards = load_state()
    if n > len(cards):
        print(f"Error: Cannot draw {n} cards, only {len(cards)} remaining.", file=sys.stderr)
        sys.exit(1)
    drawn = cards[:n]
    remaining = cards[n:]
    save_state(remaining)
    # Output as JSON array for easy parsing
    print(json.dumps(drawn))


def cmd_remaining():
    cards = load_state()
    print(len(cards))


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__.strip())
        sys.exit(1)

    command = sys.argv[1].lower()

    if command == "init":
        cmd_init()
    elif command == "draw":
        n = int(sys.argv[2]) if len(sys.argv) > 2 else 1
        cmd_draw(n)
    elif command == "remaining":
        cmd_remaining()
    else:
        print(f"Unknown command: {command}", file=sys.stderr)
        print("Commands: init, draw [N], remaining")
        sys.exit(1)
