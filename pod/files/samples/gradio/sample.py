"""A Gradio app: count the words and characters in a text."""
import gradio as gr


def count(text):
    words = text.split()
    return {"words": len(words), "characters": len(text), "longest word": max(words, key=len, default="")}


demo = gr.Interface(
    fn=count,
    inputs=gr.Textbox(lines=6, label="Text", value="The quick brown fox jumps over the lazy dog."),
    outputs=gr.JSON(label="Counts"),
    title="Word counter",
    analytics_enabled=False,
)

if __name__ == "__main__":
    # Address, port and URL prefix come from GRADIO_SERVER_NAME,
    # GRADIO_SERVER_PORT and GRADIO_ROOT_PATH, set by the template.
    demo.launch()
