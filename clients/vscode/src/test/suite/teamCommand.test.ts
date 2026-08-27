import * as assert from 'assert';

import { TEAM_PIPELINE, parseSlash, parseTeamCommand } from '../../slashCommands';

// THE MIRROR THAT CANNOT BE ENFORCED FROM THE GO SIDE.
//
// clients/tui/slash_clientparity_test.go compares the two CATALOGS and the two
// default SHAPES by parsing this file's source, which is enough for values. It
// cannot compare BEHAVIOUR: parseTeamCommand is thirty lines of parsing whose
// interesting rule -- the shape ends at the first space that does not follow a
// comma -- is exactly the kind of thing two hand-written implementations drift
// on. These cases are the Go table (phasenarration_test.go), restated here, so
// a divergence fails on one side or the other rather than becoming a command
// that behaves differently depending on which client you opened.
suite('team command', () => {
  test('the bare form runs the measured two-phase shape', () => {
    const got = parseTeamCommand('/team why is this slow');
    assert.strictEqual(got.ok, true);
    assert.deepStrictEqual(got.pipeline, ['researcher', 'coder']);
    assert.strictEqual(got.prompt, 'why is this slow');
  });

  test('padding around the question is trimmed', () => {
    const got = parseTeamCommand('/team    padded question   ');
    assert.strictEqual(got.ok, true);
    assert.strictEqual(got.prompt, 'padded question');
  });

  test('an explicit shape names its own phases', () => {
    const got = parseTeamCommand('/team:planner,coder fix the parser');
    assert.strictEqual(got.ok, true);
    assert.deepStrictEqual(got.pipeline, ['planner', 'coder']);
    assert.strictEqual(got.prompt, 'fix the parser');
  });

  // The rule that earns its keep. Cutting at the first space reads the shape as
  // ['researcher'] and swallows "coder" as the first word of the question --
  // and "researcher, coder" with a space is what a person actually types.
  test('a space after a comma stays inside the shape', () => {
    const got = parseTeamCommand('/team:researcher, coder why');
    assert.strictEqual(got.ok, true);
    assert.deepStrictEqual(got.pipeline, ['researcher', 'coder']);
    assert.strictEqual(got.prompt, 'why');
  });

  // What roles exist is daemon/roles.go's fact. A client that validated the
  // list would be the reason a newly added fifth role does not work.
  test('unknown role names are passed to the daemon, not refused here', () => {
    const got = parseTeamCommand('/team:nosuchrole hello');
    assert.strictEqual(got.ok, true);
    assert.deepStrictEqual(got.pipeline, ['nosuchrole']);
  });

  test('anything that is not a team command passes through byte for byte', () => {
    for (const raw of [
      '/team',
      '/teamwork on this',
      '/team:',
      '/team:coder',
      '/team: ',
      '/team: question',
      '/team:coder ',
      'what does /team do',
      'ordinary question',
      '/',
    ]) {
      const got = parseTeamCommand(raw);
      assert.strictEqual(got.ok, false, `${raw} was treated as a team command`);
      assert.strictEqual(got.prompt, raw, `${raw} was rewritten to ${got.prompt}`);
      assert.deepStrictEqual(got.pipeline, [], `${raw} asked for a pipeline`);
    }
  });

  // The ordering trap, from the VS Code side: onPrompt runs parseSlash before
  // parseTeamCommand, so a catalog entry with no matching exclusion would route
  // /team as an ordinary steered command -- raw text, no pipeline, every other
  // test still green.
  test('parseSlash hands the team command onward', () => {
    for (const raw of ['/team why is this slow', '/team:researcher,coder why']) {
      const got = parseSlash(raw);
      assert.strictEqual(got.rawPassthrough, true, `${raw} was captured by parseSlash`);
    }
  });

  test('the bare form asks for usage rather than becoming a prompt', () => {
    const got = parseSlash('/team');
    assert.strictEqual(got.rawPassthrough, false);
    assert.strictEqual(got.usageOnly, true);
    assert.strictEqual(got.def?.name, 'team');
  });

  test('the default shape is not the shape that lost the A/B', () => {
    assert.ok(!TEAM_PIPELINE.includes('planner'));
    assert.ok(!TEAM_PIPELINE.includes('tester'));
  });
});
