Output docs/demo.gif
Output docs/demo.mp4

Set Shell "bash"
Set FontFamily "monospace"
Set FontSize 22
Set Width 1120
Set Height 700
Set Theme "Catppuccin Mocha"
Set Framerate 15
Set CursorBlink false
Set TypingSpeed 45ms

Hide
Type `bash "$DEMO_REPO/demo/session.sh"`
Enter
Wait+Line /demo:~\$$/
Show
Sleep 2s

Type "gh autoaccount setup --native --no-shell-rc > /dev/null"
Enter
Wait+Line /demo:.*\$$/
Sleep 1s
Type `export PATH="$HOME/.local/bin:$PATH"`
Enter
Wait+Line /demo:.*\$$/
Sleep 1s
Type "gh autoaccount default alice"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s
Type "gh autoaccount pin bob --dir ~/work-project > /dev/null"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s

Type "cd ~/personal"
Enter
Wait+Line /demo:.*\$$/
Type "gh api /user --jq .login"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s
Type "cd ~/work-project/src"
Enter
Wait+Line /demo:.*\$$/
Type "gh api /user --jq .login"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s
Type "cd ~/personal"
Enter
Wait+Line /demo:.*\$$/
Type "gh api /user --jq .login"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s
Type "# No global account switching. No .envrc."
Enter
Sleep 3s
