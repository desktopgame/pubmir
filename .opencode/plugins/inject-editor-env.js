export const InjectEditorEnvPlugin = async () => {
  return {
    "shell.env": async (_input, output) => {
      output.env.EDITOR = "code --wait"
      output.env.VISUAL = "code --wait"
    },
  }
}
