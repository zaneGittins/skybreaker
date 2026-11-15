// A tool call whose arguments never reached pi. Some providers drop a
// call's arguments while converting the model's tool-call format, and pi
// then reports every required field as missing, which the model takes for
// its own formatting mistake and works around (cutting a file into
// three-line appends, say) instead of retrying. pi repairs malformed and
// cut-off JSON, so a call the model wrote badly still keeps some fields:
// only "Received arguments: {}" means they were lost on the way. This tells
// the model so.

const lost = /^Validation failed for tool "([^"\n]{1,128})":\n[\s\S]*\n\nReceived arguments:\n\{\}$/;

export default function (pi) {
  pi.on('message_end', (event) => {
    const m = event.message;
    if (m?.role !== 'toolResult' || m.isError !== true || !Array.isArray(m.content) || m.content.length !== 1) return;
    const text = m.content[0]?.type === 'text' ? m.content[0].text : '';
    const match = typeof text === 'string' && text.match(lost);
    if (!match) return;
    const tool = match[1];
    return {
      message: {
        ...m,
        content: [{
          type: 'text',
          text: `The ${tool} call reached pi with no arguments: the model provider dropped them on the way, ` +
            `so this is not your formatting. Retry the same call once. If it is lost again, the provider is ` +
            `likely mangling something in the arguments, such as a long block of code or text that looks like ` +
            `a stream marker ("data:", "[DONE]"). Change how you write that part, for example by building ` +
            `the string from pieces or writing the file in two or three larger parts, rather than cutting it ` +
            `into many small calls.`,
        }],
      },
    };
  });
}
