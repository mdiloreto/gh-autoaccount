Output docs/demo.gif
Output docs/demo.mp4

Set Shell "bash"
Set FontFamily "monospace"
Set FontSize 24
Set Width 1280
Set Height 720
Set Theme "Catppuccin Mocha"
Set Framerate 20
Set CursorBlink false
Set TypingSpeed 35ms

Hide
Type `bash "$DEMO_REPO/demo/session.sh"`
Enter
Wait+Line /demo:~\$$/
Type "gh autoaccount setup --no-shell-rc > /dev/null"
Enter
Wait+Line /demo:.*\$$/
Type `export PATH="$HOME/.local/bin:$PATH"`
Enter
Wait+Line /demo:.*\$$/
Type `demo_scene '01 / Pin once' 'Setup is complete. Choose your personal and work accounts.'`
Enter
Wait+Line /demo:.*\$$/
Show
Sleep 1s
Type "gh autoaccount default alice"
Enter
Wait+Line /demo:.*\$$/
Sleep 1s
Type "gh autoaccount pin bob --dir ~/work"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s

Hide
Type `demo_scene '02 / Switch projects, not accounts' 'The same command. The account for each directory.'`
Enter
Wait+Line /demo:.*\$$/
Show
Sleep 1s
Type "cd ~/personal"
Enter
Wait+Line /demo:.*\$$/
Type "gh api /user --jq .login"
Enter
Wait+Line /demo:.*\$$/
Sleep 1.5s
Type "cd ~/work/src"
Enter
Wait+Line /demo:.*\$$/
Type "gh api /user --jq .login"
Enter
Wait+Line /demo:.*\$$/
Sleep 1.5s
Type "cd ~/personal"
Enter
Wait+Line /demo:.*\$$/
Type "gh api /user --jq .login"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s

Hide
Type "cd ~/work/src"
Enter
Wait+Line /demo:.*\$$/
Type `demo_scene '03 / Give agent commands an account guard' 'Run only when the selected account matches your expectation.'`
Enter
Wait+Line /demo:.*\$$/
Show
Sleep 1s
Type `gh autoaccount exec --expect-user bob -- \`
Enter
Type "  gh api /user --jq .login"
Enter
Wait+Line /demo:.*\$$/
Sleep 2s
Type "# No global switching. No direnv."
Enter
Sleep 2s

Hide
Type "cd ~"
Enter
Wait+Line /demo:.*\$$/
Type `demo_scene 'Try the community beta' 'github.com/mdiloreto/gh-autoaccount'`
Enter
Wait+Line /demo:.*\$$/
Show
Sleep 500ms
Type "# Install in your own terminal:"
Enter
Type "gh extension install mdiloreto/gh-autoaccount"
Sleep 4s
