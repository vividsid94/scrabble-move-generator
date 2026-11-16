# Quackle win table (temporary)

`quackle_bogowin.txt` is Quackle's `bogowin` win-percentage table, copied unchanged from
https://github.com/quackle/quackle (`data/strategy/default/bogowin`, master).

- Copyright (C) 2005-2019 Jason Katz-Brown, John O'Laughlin, and John Fultz.
- Licensed under the GNU General Public License v3 (or later): https://www.gnu.org/licenses/gpl-3.0.html

It is used only inside this private, server-side service (`champ_sim.go`), and is never sent to
clients. It is a stand-in until Whiffers has its own table built from Wally self-play (see
`CHAMPIONSHIP_PLAYER_PLAN.md` in the whiffers repo); remove this file and this note then.

Format: one line per cell, `lead unseen winChance`, leads -300..300, unseen 1..93 - the chance that
the player about to move wins, leading by `lead` (score plus leave values) with `unseen` tiles not
on their own rack.
