import {
  app,
  el,
  section,
  details,
  field,
  check,
  button,
  request,
  mutate,
  all,
  options,
  can,
  message,
  poll,
  confirm,
  inspect,
  context,
} from "./ui.js";
import { launchForm, preparations } from "./launch.js";
import { editor as sourceEditor } from "./editor.js";
import { initialize } from "./ui.js";
await initialize(async () => {
  const author = can("template.create");
  app.prepend(
    el(
      "p",
      "Reusable lab sources, build recipes, and pinned dependencies. Portable exports contain source files needed to recreate images.",
    ),
  );
  const browse = section("Published templates");
  const selection = field(browse, "Template", "select"),
    revision = field(browse, "Revision", "select");
  const description = el("p");
  browse.append(description);
  const sourceView = details("View source", browse),
    sourceText = el("pre");
  sourceView.append(sourceText);
  let templates = [],
    selected = null,
    loadedRevision = "",
    generation = 0;
  const launch = details("Launch this template");
  const ownerSelect = field(launch, "Lab owner", "select");
  options(ownerSelect, [{ id: "me", label: "Just me" }]);
  ownerSelect.value = "me";
  try {
    const groups = await all("groups.list", {}, "after");
    for (const group of groups.filter((g) => g.joined))
      ownerSelect.append(el("option", group.name, { value: group.id }));
  } catch {
    /* Core launch remains available when groups are inaccessible. */
  }
  const launcher = await launchForm(launch, {
    owner: () =>
      ownerSelect.value === "me"
        ? { kind: "user", id: context.subjectId }
        : { kind: "team", id: ownerSelect.value },
  });
  // Context is supplied by the authenticated host, never entered by the package.
  const browseActions = el("div", "", { class: "actions" });
  browse.append(browseActions);
  browseActions.append(
    button("Export sources", async () => {
      if (!revision.value) throw new Error("Select a published revision.");
      await request("templates.download", {
        templateId: selection.value,
        revisionId: revision.value,
      });
      message("Source archive exported.");
    }),
  );
  let name,
    descriptionInput,
    source,
    editor,
    archive = "",
    preview,
    previewBox,
    publishArchive,
    editorMode,
    importGeneration = 0,
    pendingImport,
    pendingCopy;
  async function loadRevision() {
    const token = ++generation;
    loadedRevision = "";
    if (source) source.value = "";
    editorMode?.reset();
    sourceText.textContent = "";
    if (!revision.value) return;
    const content = await request("templates.content", {
      templateId: selection.value,
      revisionId: revision.value,
    });
    if (token !== generation) return;
    loadedRevision = revision.value;
    sourceText.textContent = content.source;
    if (source) source.value = content.source;
    await launcher.select(selection.value, revision.value);
  }
  async function choose() {
    const token = ++generation;
    loadedRevision = "";
    selected = templates.find((t) => t.id === selection.value);
    description.textContent = selected?.description ?? "";
    if (name) {
      name.value = selected?.name ?? "";
      descriptionInput.value = selected?.description ?? "";
    }
    const revisions = selection.value
      ? await all("templates.revisions", { templateId: selection.value })
      : [];
    if (token !== generation) return;
    options(
      revision,
      revisions
        .sort((a, b) => b.revisionNumber - a.revisionNumber)
        .map((r) => ({ id: r.id, label: `Revision ${r.revisionNumber}` })),
    );
    revision.value = revisions[0]?.id ?? "";
    await loadRevision();
  }
  async function refreshTemplates() {
    templates = await all("templates.list");
    options(selection, templates);
    options(launcher.template, templates);
    if (!selection.value) selection.value = templates[0]?.id ?? "";
    await choose();
  }
  selection.onchange = () => choose().catch((e) => message(e.message, true));
  revision.onchange = () =>
    loadRevision().catch((e) => message(e.message, true));
  if (author) {
    editor = details("Author a template");
    name = field(editor, "Template name", "input", {
      maxlength: 63,
      required: true,
    });
    descriptionInput = field(
      editor,
      "Description and instructions",
      "textarea",
      { maxlength: 4096 },
    );
    source = field(editor, "Lab YAML", "textarea", {
      class: "source",
      spellcheck: false,
    });
    editorMode = sourceEditor(editor, source);
    editor.append(
      el(
        "p",
        "Published revisions are immutable. Make an independent copy before changing synchronized content.",
      ),
    );
    const actions = el("div", "", { class: "actions" });
    editor.append(actions);
    actions.append(
      button("New template", () => {
        selection.value = "";
        selected = null;
        name.value = "";
        descriptionInput.value = "";
        source.value = "";
        editorMode.reset();
        options(revision, []);
      }),
      button("Make independent copy", async () => {
        if (!selected) throw new Error("Select a template first.");
        if (!revision.value) throw new Error("Select a published revision.");
        const original = { ...selected },
          revisionId = revision.value;
        if (!pendingCopy || pendingCopy.revisionId !== revisionId) {
          const exported = await request("templates.export", {
            templateId: original.id,
            revisionId,
          });
          const copy = await mutate("templates.create", {
            name: `${original.name.slice(0, 57)}-copy`,
            description: original.description ?? "",
          });
          pendingCopy = { revisionId, copy, archive: exported.archive };
        }
        const copy = pendingCopy.copy;
        await mutate("templates.publishBundle", {
          templateId: copy.id,
          archive: pendingCopy.archive,
        });
        pendingCopy = undefined;
        await refreshTemplates();
        selection.value = copy.id;
        await choose();
        message("Independent copy created with the complete source closure.");
      }),
      button("Validate template", async () => {
        const result = await request("templates.validate", {
          source: source.value,
          targetId: launcher.target.value,
          inputs: launcher.readInputs(),
          acknowledged: launcher.acknowledged(),
        });
        message(
          `Template validated: ${result.resourceCount} resources. ${(result.warnings ?? []).map((w) => w.message).join(" ")}`,
        );
      }),
      button(
        "Publish revision",
        async () => {
          if (selected && revision.value && loadedRevision !== revision.value)
            throw new Error("Wait for the selected source to load.");
          const draft = {
              name: name.value,
              description: descriptionInput.value,
              source: source.value,
            },
            original = selected ? { ...selected } : null;
          if (!draft.name || !draft.source)
            throw new Error("Enter a name and lab YAML.");
          let target = original;
          if (!target) {
            target = await mutate("templates.create", {
              name: draft.name,
              description: draft.description,
            });
            selected = target;
          } else if (
            target.name !== draft.name ||
            target.description !== draft.description
          ) {
            target = await mutate("templates.update", {
              templateId: target.id,
              name: draft.name,
              description: draft.description,
              resourceVersion: target.resourceVersion,
            });
            selected = target;
          }
          const templateId = target.id;
          await mutate("templates.publish", {
            templateId,
            source: draft.source,
          });
          await refreshTemplates();
          selection.value = templateId;
          await choose();
          message("Revision published.");
        },
        "primary",
      ),
    );
    const imports = details("Import a source archive");
    const file = field(imports, "Source archive", "input", {
      type: "file",
      accept: ".tar,.tgz,.gz,application/gzip,application/x-tar",
    });
    previewBox = el("div");
    imports.append(previewBox);
    file.onchange = async () => {
      const generation = ++importGeneration;
      archive = "";
      preview = null;
      previewBox.replaceChildren();
      publishArchive.disabled = true;
      try {
        const selectedFile = file.files?.[0];
        if (!selectedFile) return;
        if (selectedFile.size > 8 << 20)
          throw new Error("Archive exceeds 8 MiB.");
        const bytes = new Uint8Array(await selectedFile.arrayBuffer());
        let binary = "";
        for (let i = 0; i < bytes.length; i += 8192)
          binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
        const candidate = btoa(binary);
        const result = await request("templates.importPreview", {
          archive: candidate,
        });
        if (generation !== importGeneration) return;
        archive = candidate;
        preview = result;
        previewBox.append(
          el(
            "p",
            `${preview.fileCount} source files · ${preview.sourceBytes} bytes`,
          ),
        );
        for (const [label, value] of [
          ["Required capabilities", preview.requiredCapabilities],
          ["Pinned dependencies", preview.dependencies],
          ["Builder requirements", preview.builders],
          ["Unresolved local bindings", preview.unresolvedBuilders],
        ]) {
          const d = details(label, previewBox);
          inspect(d, value ?? []);
        }
        publishArchive.disabled = false;
      } catch (e) {
        if (generation === importGeneration) message(e.message, true);
      }
    };
    const importName = field(imports, "Imported template name", "input", {
      maxlength: 63,
    });
    publishArchive = button(
      "Import as independent template",
      async () => {
        if (!preview || !archive || !importName.value)
          throw new Error(
            "Preview an archive and enter its new template name.",
          );
        const candidate = archive,
          candidateName = importName.value;
        if (
          !pendingImport ||
          pendingImport.archive !== candidate ||
          pendingImport.name !== candidateName
        ) {
          const created = await mutate("templates.create", {
            name: candidateName,
            description: "",
          });
          pendingImport = { archive: candidate, name: candidateName, created };
        }
        const created = pendingImport.created;
        await mutate("templates.publishBundle", {
          templateId: created.id,
          archive: candidate,
        });
        pendingImport = undefined;
        await refreshTemplates();
        selection.value = created.id;
        await choose();
        message(
          "Source archive imported. Local builder bindings remain installation-specific.",
        );
      },
      "primary",
    );
    publishArchive.disabled = true;
    imports.append(publishArchive);
    const libraries = details("GitHub libraries");
    libraries.append(
      el(
        "p",
        "Synchronization publishes validated source revisions. Builds run explicitly or when a lab is prepared.",
      ),
    );
    const binding = field(libraries, "Configured source", "select"),
      reference = field(libraries, "Repository reference", "input", {
        maxlength: 1024,
        placeholder: "Reference accepted by your configured GitHub source",
      }),
      interval = field(libraries, "Poll interval (minutes)", "input", {
        type: "number",
        min: 1,
        max: 1440,
        value: 15,
      });
    const available = (await request("sources.bindings")).items ?? [];
    options(
      binding,
      available
        .filter((b) => b.available)
        .map((b) => ({ id: b.binding, label: b.binding })),
    );
    binding.value = available.find((b) => b.available)?.binding ?? "";
    const libraryList = el("div");
    libraries.append(
      button(
        "Add library",
        async () => {
          await mutate("libraries.create", {
            binding: binding.value,
            reference: reference.value,
            intervalSeconds: Number(interval.value) * 60,
          });
          await refreshLibraries();
        },
        "primary",
      ),
      button("Resolve into selected template", async () => {
        if (!selected) throw new Error("Create or select a template first.");
        await mutate("sources.resolve", {
          templateId: selected.id,
          binding: binding.value,
          reference: reference.value,
        });
        message("Source resolution queued.");
      }),
      libraryList,
    );
    let renderedLibraries = "";
    async function refreshLibraries() {
      const values = (await request("libraries.list")).items ?? [];
      const next = JSON.stringify(values);
      if (next === renderedLibraries) return;
      renderedLibraries = next;
      libraryList.replaceChildren();
      for (const library of values) {
        const card = el("article");
        card.append(
          el("h3", library.reference),
          el(
            "p",
            library.paused
              ? `Paused: ${library.pauseReason ?? "by author"}`
              : `Polling every ${library.intervalSeconds / 60} minutes`,
          ),
          button("Sync now", async () => {
            await mutate("libraries.sync", { libraryId: library.id });
            message("Synchronization queued.");
            await refreshLibraries();
          }),
          button(
            library.paused ? "Resume polling" : "Pause polling",
            async () => {
              await request("libraries.schedule", {
                libraryId: library.id,
                paused: !library.paused,
              });
              await refreshLibraries();
            },
          ),
          button("Show entries and history", async () => {
            const data = await Promise.all([
              request("libraries.entries", { libraryId: library.id }),
              request("libraries.history", { libraryId: library.id }),
            ]);
            const detail = details("Entries and synchronization history", card);
            detail.open = true;
            inspect(detail, { entries: data[0].items, history: data[1].items });
          }),
        );
        libraryList.append(card);
      }
    }
    await refreshLibraries();
    poll(refreshLibraries, 15000);
    const builds = details("Image builds and approvals");
    const recipe = field(builds, "Recipe ID", "input", { maxlength: 64 });
    builds.append(
      button("Build image", async () => {
        if (!revision.value) throw new Error("Select a published revision.");
        await mutate("builds.create", {
          revisionId: revision.value,
          recipeId: recipe.value,
        });
        await refreshWork();
      }),
    );
    const workList = el("div");
    builds.append(workList);
    let refreshing = false,
      renderedWork = "";
    async function refreshWork() {
      if (refreshing) return;
      refreshing = true;
      try {
        const work = (await request("builds.list")).items ?? [];
        const next = JSON.stringify(work);
        if (next === renderedWork) return;
        renderedWork = next;
        workList.replaceChildren();
        for (const w of work) {
          const card = el("article");
          card.append(
            el("h3", `${w.request.kind} · ${w.result?.stage ?? w.state}`),
            el("small", w.id),
          );
          if (w.failureCode)
            card.append(el("p", w.failureCode, { class: "error" }));
          if (["queued", "running"].includes(w.state))
            card.append(
              button("Cancel", async () => {
                await mutate("builds.cancel", { operationId: w.id });
                await refreshWork();
              }),
            );
          if (w.request.kind === "build" && w.state === "succeeded")
            card.append(
              button("Import to environment", async () => {
                await mutate("images.import", {
                  buildId: w.id,
                  targetId: launcher.target.value,
                });
                await refreshWork();
              }),
            );
          if (w.request.kind === "image-import" && w.state === "succeeded")
            card.append(
              button("Approve image", async () => {
                await mutate("images.approve", {
                  imageId: w.id,
                  approved: true,
                  retired: false,
                });
                message(
                  "Image approved. Waiting preparations will recheck admission.",
                );
              }),
              button(
                "Retire image",
                async () => {
                  if (
                    await confirm(
                      "Retire this image?",
                      "New deployments will be blocked. Existing deployments retain their image until cleanup.",
                      [w.id],
                    )
                  ) {
                    await mutate("images.approve", {
                      imageId: w.id,
                      approved: false,
                      retired: true,
                    });
                    message("Image retirement queued.");
                  }
                },
                "danger",
              ),
            );
          if (w.result) {
            const data = details("Result", card);
            inspect(data, w.result);
          }
          workList.append(card);
        }
      } finally {
        refreshing = false;
      }
    }
    await refreshWork();
    poll(refreshWork);
  }
  browseActions.append(button("Refresh templates", refreshTemplates));
  await refreshTemplates();

  preparations(app);
});
