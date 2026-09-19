import { el, field, button, check, request, message } from "./ui.js";
// A bounded projection of the canonical authoring schema. Core validates the document.
function resolve(schema, root, depth = 0, budget = { left: 256 }) {
  if (--budget.left < 0 || !schema || typeof schema !== "object" || depth > 16)
    return {};
  if (schema.$ref) {
    if (typeof schema.$ref !== "string" || !schema.$ref.startsWith("#/"))
      return {};
    let value = root;
    for (const segment of schema.$ref.slice(2).split("/")) {
      const key = segment.replaceAll("~1", "/").replaceAll("~0", "~");
      if (!value || typeof value !== "object" || !Object.hasOwn(value, key))
        return {};
      value = value[key];
    }
    return resolve(value, root, depth + 1, budget);
  }
  if (Array.isArray(schema.allOf)) {
    const parts = schema.allOf.map((p) => resolve(p, root, depth + 1, budget));
    if (parts.every((p) => p.type === "object"))
      return {
        ...schema,
        allOf: undefined,
        type: "object",
        properties: Object.assign({}, ...parts.map((p) => p.properties)),
        required: [...new Set(parts.flatMap((p) => p.required ?? []))],
      };
  }
  return schema;
}
function initial(schema, root, depth = 0) {
  const s = resolve(schema, root);
  if (depth > 12) return null;
  if (s.const !== undefined) return structuredClone(s.const);
  if (s.default !== undefined) return structuredClone(s.default);
  if (s.enum?.length) return s.enum[0];
  if (s.type === "object")
    return Object.fromEntries(
      (s.required ?? []).map((k) => [
        k,
        initial(s.properties?.[k] ?? {}, root, depth + 1),
      ]),
    );
  if (s.type === "array") return [];
  if (s.type === "boolean") return false;
  if (["number", "integer"].includes(s.type)) return s.minimum ?? 0;
  return "";
}
function render(
  parent,
  schema,
  root,
  value,
  label,
  required,
  changed,
  depth = 0,
) {
  const s = resolve(schema, root),
    properties = Object.entries(s.properties ?? {}).slice(0, 128);
  const update = (next) => {
    value = next;
    changed(next);
  };
  const redraw = () => {
    parent.replaceChildren();
    render(parent, schema, root, value, label, required, changed, depth);
  };
  if (
    depth > 12 ||
    s.oneOf ||
    s.anyOf ||
    s.allOf ||
    (!s.type && !s.enum && s.const === undefined) ||
    Array.isArray(s.type) ||
    (s.type === "object" && !properties.length)
  ) {
    const input = field(parent, `${label} (JSON)`, "textarea");
    input.value = JSON.stringify(value, null, 2);
    input.oninput = () => {
      try {
        update(JSON.parse(input.value));
        input.setCustomValidity("");
      } catch {
        input.setCustomValidity("Enter valid JSON.");
      }
    };
    return;
  }
  if (s.type === "object") {
    const group = el("fieldset");
    group.append(el("legend", label));
    parent.append(group);
    const object = () =>
      value && typeof value === "object" && !Array.isArray(value) ? value : {};
    const set = (key, next) => {
      const result = { ...object() };
      if (next === undefined) delete result[key];
      else
        Object.defineProperty(result, key, {
          value: next,
          writable: true,
          configurable: true,
          enumerable: true,
        });
      update(result);
    };
    for (const [key, child] of properties) {
      const needed = s.required?.includes(key) ?? false;
      if (needed || Object.hasOwn(object(), key)) {
        const row = el("div");
        group.append(row);
        render(
          row,
          child,
          root,
          object()[key],
          key,
          needed,
          (next) => set(key, next),
          depth + 1,
        );
        if (!needed)
          row.append(
            button(`Remove ${key}`, () => {
              set(key, undefined);
              redraw();
            }),
          );
      } else
        group.append(
          button(`Add ${key}`, () => {
            set(key, initial(child, root));
            redraw();
          }),
        );
    }
    if (
      Object.keys(object()).some((k) => !properties.some(([key]) => key === k))
    )
      group.append(
        el(
          "small",
          "Additional fields are preserved. Use source mode to edit them.",
        ),
      );
  } else if (s.type === "array") {
    const group = el("fieldset");
    group.append(el("legend", label));
    parent.append(group);
    const values = () => (Array.isArray(value) ? value : []);
    values()
      .slice(0, 256)
      .forEach((item, index) => {
        const row = el("div");
        group.append(row);
        render(
          row,
          s.items ?? {},
          root,
          item,
          `${label} ${index + 1}`,
          false,
          (next) => update(values().map((v, i) => (i === index ? next : v))),
          depth + 1,
        );
        row.append(
          button(`Remove item ${index + 1}`, () => {
            update(values().filter((_, i) => i !== index));
            redraw();
          }),
        );
      });
    const add = button(`Add ${label} item`, () => {
      update([...values(), initial(s.items ?? {}, root)]);
      redraw();
    });
    add.disabled = values().length >= Math.min(s.maxItems ?? 256, 256);
    group.append(add);
  } else if (s.const !== undefined)
    parent.append(el("p", `${label}: ${s.const}`));
  else if (s.enum) {
    const input = field(parent, label + (required ? " *" : ""), "select");
    s.enum.forEach((v) =>
      input.append(el("option", String(v), { value: JSON.stringify(v) })),
    );
    input.value = JSON.stringify(value);
    input.onchange = () => update(JSON.parse(input.value));
  } else if (s.type === "boolean") {
    const input = check(parent, label, value === true);
    input.onchange = () => update(input.checked);
  } else {
    const numeric = ["number", "integer"].includes(s.type);
    const input = field(parent, label + (required ? " *" : ""), "input", {
      type: numeric ? "number" : s.writeOnly ? "password" : "text",
      min: s.minimum,
      max: s.maximum,
      step: s.type === "integer" ? 1 : "any",
      minlength: s.minLength,
      maxlength: s.maxLength,
      autocomplete: "off",
    });
    input.required = required;
    input.value = value ?? "";
    input.oninput = () =>
      update(
        numeric
          ? input.value === ""
            ? null
            : input.valueAsNumber
          : input.value,
      );
  }
  if (s.description) parent.append(el("small", s.description));
}
export function editor(parent, source) {
  let schema;
  const actions = el("div", "", { class: "actions" }),
    form = el("div");
  source.parentElement.before(actions, form);
  form.hidden = true;
  const sourceMode = () => {
    form.hidden = true;
    source.parentElement.hidden = false;
  };
  actions.append(
    button("Form", async () => {
      schema ??= await request("templates.authoringSchema");
      const document = source.value.trim()
        ? await request("templates.authoringDocument", { source: source.value })
        : initial(schema, schema);
      form.replaceChildren(
        el(
          "p",
          "Form edits use JSON, which is valid YAML. Source comments are not retained.",
        ),
      );
      const fields = el("div");
      form.append(fields);
      render(
        fields,
        schema,
        schema,
        document,
        "Lab",
        false,
        (value) => (source.value = JSON.stringify(value, null, 2)),
      );
      form.hidden = false;
      source.parentElement.hidden = true;
    }),
    button("Source", sourceMode),
  );
  return { reset: sourceMode };
}

export function providerOptions(parent, target) {
  let value,
    rawMode = false;
  const panel = el("section");
  parent.append(panel);
  const content = el("div");
  panel.append(el("h3", "Plugin options"), content);
  const schema = () => {
    try {
      const encoded = target()?.discovery?.override_json_schema;
      if (typeof encoded !== "string" || encoded.length > 2000000) return;
      return JSON.parse(
        new TextDecoder().decode(
          Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0)),
        ),
      );
    } catch {
      return;
    }
  };
  const draw = () => {
    content.replaceChildren();
    if (value === undefined) {
      content.append(
        el("p", "Using template and environment defaults."),
        button("Customize options", () => {
          const s = schema();
          value = s ? initial(s, s) : {};
          rawMode = !s;
          draw();
        }),
      );
      return;
    }
    content.append(
      button("Use template defaults", () => {
        value = undefined;
        draw();
      }),
    );
    const s = schema();
    if (s && !rawMode) {
      const fields = el("div");
      content.append(fields);
      render(
        fields,
        s,
        s,
        value,
        "Provider override",
        false,
        (next) => (value = next),
      );
    } else {
      const input = field(content, "Provider override JSON", "textarea");
      input.value = JSON.stringify(value, null, 2);
      input.oninput = () => {
        try {
          const next = JSON.parse(input.value);
          if (!next || typeof next !== "object" || Array.isArray(next))
            throw new Error();
          value = next;
          input.setCustomValidity("");
        } catch {
          input.setCustomValidity("Enter a JSON object.");
        }
      };
    }
    if (s)
      content.append(
        button(rawMode ? "Use form" : "Edit JSON", () => {
          rawMode = !rawMode;
          draw();
        }),
      );
  };
  draw();
  return {
    read: () => value,
    reset: () => {
      value = undefined;
      draw();
    },
  };
}
