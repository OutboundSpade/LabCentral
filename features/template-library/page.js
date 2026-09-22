import { renderNotes } from "./notes.js";
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
  tabs,
  modal,
  setDirty,
  stableList,
} from "./ui.js";
import { launchForm, preparations } from "./launch.js";
import { editor as sourceEditor } from "./editor.js";
import { initialize } from "./ui.js";
await initialize(async () => {
  const author = can("template.create"),
    imageAdmin = !!context.imageAdministrator;
  const views = tabs([
    "Browse",
    ...(author ? ["Author", "Import", "Libraries"] : []),
    ...(author || imageAdmin ? ["Images"] : []),
    "Activity",
  ]);
  const panels = views.panels;
  let baseline = "",
    dirty = false,
    metadata = {},
    recipesControl,
    recipeInfo,
    publishButton;
  const draftValue = () =>
    JSON.stringify([name?.value, descriptionInput?.value, source?.value]);
  async function markDraft() {
    const next = draftValue() !== baseline;
    if (dirty !== next) {
      dirty = next;
      await setDirty(dirty);
    }
  }
  async function cleanDraft() {
    baseline = draftValue();
    dirty = false;
    await setDirty(false);
  }
  async function discardDraft() {
    if (!dirty) return true;
    return confirm(
      "Discard unsaved changes?",
      "Your unpublished edits will be lost.",
      [],
      "Discard changes",
    );
  }

  const browse = section("Published templates", panels.Browse);
  const search = field(browse, "Search templates", "input", {
    type: "search",
    placeholder: "Search by name or description",
  });
  search.oninput = () => {
    const matched = templates.filter((t) =>
      (t.name + " " + t.description)
        .toLowerCase()
        .includes(search.value.toLowerCase()),
    );
    if (selected && !matched.some((t) => t.id === selected.id))
      matched.unshift(selected);
    options(selection, matched, "Choose a template");
  };
  const selection = field(browse, "Template", "select"),
    revision = field(browse, "Revision", "select");
  const description = el("div");
  browse.append(description);
  const sourceView = details("View source", browse),
    sourceText = el("pre");
  sourceView.append(sourceText);
  let templates = [],
    selected = null,
    loadedRevision = "",
    generation = 0;
  const launch = section("Launch this template", panels.Browse);
  const launchSummary = el("p", "", { class: "font-medium" });
  launch.append(launchSummary);
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
  launcher.template.parentElement.hidden = true;
  launcher.revision.parentElement.hidden = true;
  const activity = preparations(panels.Activity);
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
    if (!revision.value) {
      await launcher.select("", "");
      launchSummary.textContent = "Choose a published template to launch.";
      return;
    }
    const content = await request("templates.content", {
      templateId: selection.value,
      revisionId: revision.value,
    });
    if (token !== generation) return;
    loadedRevision = revision.value;
    metadata = content;
    launchSummary.textContent = `${selected?.name ?? ""} · ${revision.selectedOptions[0]?.textContent ?? ""}`;
    if (recipesControl) {
      options(
        recipesControl,
        (content.recipes ?? []).map((r) => ({
          id: r.id,
          label: `${r.id} · ${r.builder}${r.available ? "" : " · Builder unavailable"}`,
        })),
        "Choose a recipe",
      );
      recipesControl.value = content.recipes?.[0]?.id ?? "";
      recipesControl.onchange?.();
    }
    if (publishButton) publishButton.disabled = !!content.sourceArchive;
    if (source) source.readOnly = !!content.sourceArchive;
    sourceText.textContent = content.source;
    if (source) source.value = content.source;
    await launcher.select(selection.value, revision.value);
    await cleanDraft();
  }
  async function choose() {
    const token = ++generation;
    loadedRevision = "";
    selected = templates.find((t) => t.id === selection.value);
    renderNotes(description, selected?.description ?? "");
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
    if (!dirty) await choose();
  }
  selection.onchange = async () => {
    if (await discardDraft())
      await choose().catch((e) => message(e.message, true));
    else selection.value = selected?.id ?? "";
  };
  revision.onchange = async () => {
    if (await discardDraft())
      await loadRevision().catch((e) => message(e.message, true));
    else revision.value = loadedRevision;
  };
  if (author) {
    editor = section("Author a template", panels.Author);
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
    editor.addEventListener("input", () =>
      markDraft().catch((e) => message(e.message, true)),
    );
    const actions = el("div", "", { class: "actions" });
    editor.append(actions);
    actions.append(
      button("New template", async () => {
        if (!(await discardDraft())) return;
        metadata = {};
        source.readOnly = false;
        publishButton.disabled = false;
        selection.value = "";
        selected = null;
        name.value = "";
        descriptionInput.value = "";
        source.value = "";
        editorMode.reset();
        options(revision, []);
        await launcher.select("", "");
        launchSummary.textContent = "Choose a published template to launch.";
        description.textContent = "";
        sourceText.textContent = "";
        await cleanDraft();
      }),
      button("Use as starting point", async () => {
        if (!selected || !loadedRevision)
          throw new Error("Choose a published template in Browse first.");
        if (metadata.sourceArchive)
          throw new Error(
            "Use Make independent copy to preserve this template’s complete source archive.",
          );
        if (!(await discardDraft())) return;
        const originalName = selected.name;
        selected = null;
        selection.value = "";
        metadata = {};
        loadedRevision = "";
        options(revision, []);
        name.value = (originalName + " copy").slice(0, 63);
        source.readOnly = false;
        publishButton.disabled = false;
        await markDraft();
        name.focus();
      }),
      button("Make independent copy", async () => {
        if (!(await discardDraft())) return;
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
        await cleanDraft();
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
      (publishButton = button(
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
          if (metadata.sourceArchive)
            throw new Error(
              "Export and edit the complete source archive, then import it as an independent template.",
            );
          const templateId = target.id;
          await mutate("templates.publish", {
            templateId,
            source: draft.source,
          });
          await cleanDraft();
          await refreshTemplates();
          selection.value = templateId;
          await choose();
          message("Revision published.");
        },
        "primary",
      )),
    );
    const imports = section("Import a source archive", panels.Import);
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
          const items = value ?? [];
          if (!items.length) d.append(el("p", "None"));
          for (const item of items)
            d.append(
              el(
                "p",
                typeof item === "string"
                  ? item
                  : [
                      item.id ?? item.name ?? item.mount ?? item.package,
                      item.kind,
                      item.schema,
                      item.digest,
                    ]
                      .filter(Boolean)
                      .join(" · "),
              ),
            );
          if (label === "Unresolved local bindings" && items.length)
            d.append(
              el(
                "p",
                "Ask an administrator to bind these builder requirements in this installation before building. Import preserves the portable source unchanged.",
              ),
            );
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
        if (!(await discardDraft())) return;
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
        await cleanDraft();
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
    const libraries = section("GitHub libraries", panels.Libraries);
    libraries.append(
      el(
        "p",
        "Synchronization publishes validated source revisions. Builds run explicitly or when a lab is prepared.",
      ),
    );
    const binding = field(libraries, "Configured source", "select"),
      reference = field(libraries, "Repository reference", "input", {
        maxlength: 1024,
        placeholder: "main",
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
    libraries.append(
      el(
        "p",
        "Enter the branch, tag or commit configured by your operator, for example main. The repository and read credential belong to the selected source binding.",
        { class: "muted" },
      ),
    );
    const configured = available.some((b) => b.available);
    if (!configured)
      libraries.prepend(
        el(
          "p",
          "No GitHub source is configured for this workspace. Ask an administrator to configure a repository and grant its source binding here. No repository credential is entered on this page.",
          { role: "status" },
        ),
      );
    const libraryList = el("div");
    libraries.append(
      button(
        "Add library",
        async () => {
          if (!binding.value || !reference.value.trim())
            throw new Error(
              "Choose a configured source and enter its configured branch, tag or commit.",
            );
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
    if (!configured)
      for (const b of libraries.querySelectorAll("button")) b.disabled = true;
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
          el(
            "small",
            `Next poll: ${new Date(library.nextSyncAt).toLocaleString()}`,
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
            for (const entry of data[0].items ?? [])
              detail.append(
                el(
                  "p",
                  `${entry.key} · ${entry.available ? "Available upstream" : "Removed upstream"} · Commit ${entry.commit}`,
                ),
              );
            for (const work of data[1].items ?? [])
              detail.append(
                el(
                  "p",
                  `${new Date(work.createdAt).toLocaleString()} · ${work.state}${work.failureCode ? ` · ${work.failureCode}` : ""} · ${work.result?.resolvedIdentity ?? "Commit pending"}`,
                ),
              );
            if (!(data[1].items ?? []).length)
              detail.append(el("p", "No synchronization has completed yet."));
          }),
        );
        libraryList.append(card);
      }
    }
    await refreshLibraries();
    poll(refreshLibraries, 15000);
  }
  if (author || imageAdmin) {
    const builds = section("Build recipes", panels.Images);
    builds.append(
      el(
        "p",
        "Recipes come from the template selected in Browse. Imports and approvals apply to the environment shown here.",
        { class: "muted" },
      ),
    );
    recipesControl = field(builds, "Build recipe", "select");
    recipeInfo = el("p");
    builds.append(recipeInfo);
    recipesControl.onchange = () => {
      const r = (metadata.recipes ?? []).find(
        (r) => r.id === recipesControl.value,
      );
      recipeInfo.textContent = r
        ? `${r.architecture} · ${r.available ? "Builder available" : "Builder unavailable — ask an administrator to configure the required binding"}${r.dependsOn?.length ? ` · Depends on ${r.dependsOn.join(", ")}` : ""}`
        : "This template uses existing images and has no build recipes.";
    };
    const environment = field(builds, "Image environment", "select");
    options(environment, (await request("targets.list")).items ?? []);
    environment.value = launcher.target.value;
    if (author)
      builds.append(
        button(
          "Build image",
          async () => {
            const r = (metadata.recipes ?? []).find(
              (r) => r.id === recipesControl.value,
            );
            if (!r) {
              recipesControl.focus();
              throw new Error(
                "Choose a template with build recipes in Browse, then select its recipe.",
              );
            }
            if (!r.available)
              throw new Error(
                "The required builder is unavailable. Ask an administrator to configure its binding.",
              );
            await mutate("builds.create", {
              revisionId: revision.value,
              recipeId: r.id,
            });
            message("Image build queued.");
            await refreshWork();
          },
          "primary",
        ),
      );
    const workPanel = section("Builds and imported images", panels.Images),
      workList = el("div", "", { class: "space-y-3" });
    workPanel.append(workList);
    let pending = false;
    async function refreshWork() {
      if (pending) return;
      pending = true;
      try {
        const work = (await request("builds.list")).items ?? [];
        const visible = work.filter(
          (w) =>
            ["build", "image-import"].includes(w.request.kind) &&
            !w.request.import?.retireBindingId,
        );
        const rows = [];
        for (const w of visible) {
          let image = null;
          if (w.request.kind === "image-import" && w.state === "succeeded")
            try {
              image = await request("images.get", { imageId: w.id });
            } catch {
              image = { unavailable: true };
            }
          rows.push({ ...w, image });
        }
        stableList(workList, rows, (w) => {
          const card = el("article");
          const recipe =
            w.request.build?.recipe?.id ?? w.request.build?.recipeId;
          const stage = w.image?.retired
            ? "Retired"
            : w.image?.approved
              ? "Approved"
              : w.image?.unavailable
                ? "Image unavailable"
                : w.image
                  ? "Awaiting approval"
                  : (w.result?.stage ?? w.state);
          card.append(
            el(
              "h3",
              `${recipe ?? (w.request.kind === "build" ? "Image build" : "Imported image")} · ${stage}`,
            ),
            el(
              "p",
              `${new Date(w.createdAt).toLocaleString()}${w.image?.targetId ? ` · Environment: ${w.image.targetId}` : ""}`,
            ),
          );
          if (w.failureCode)
            card.append(el("p", w.failureCode, { class: "error" }));
          const actions = el("div", "", { class: "actions" });
          card.append(actions);
          if (author && ["queued", "running"].includes(w.state))
            actions.append(
              button("Cancel build", async () => {
                await mutate("builds.cancel", { operationId: w.id });
                await refreshWork();
              }),
            );
          if (
            imageAdmin &&
            w.request.kind === "build" &&
            w.state === "succeeded"
          )
            actions.append(
              button("Import to environment", async () => {
                if (!environment.value)
                  throw new Error("Select the image environment first.");
                await mutate("images.import", {
                  buildId: w.id,
                  targetId: environment.value,
                });
                message("Image import queued.");
                await refreshWork();
              }),
            );
          if (
            imageAdmin &&
            w.image &&
            !w.image.unavailable &&
            !w.image.retired
          ) {
            if (!w.image.approved)
              actions.append(
                button(
                  "Approve image",
                  async () => {
                    if (
                      !(await confirm(
                        "Approve this image?",
                        "Review the image evidence and target before allowing new deployments.",
                        [
                          w.image.targetId,
                          w.image.binding?.artifactDigest ?? w.id,
                        ],
                        "Approve image",
                      ))
                    )
                      return;
                    await mutate("images.approve", {
                      imageId: w.id,
                      approved: true,
                      retired: false,
                    });
                    await refreshWork();
                  },
                  "primary",
                ),
              );
            actions.append(
              button(
                "Retire image",
                async () => {
                  if (
                    await confirm(
                      "Retire this image?",
                      "New deployments will be blocked. Existing labs retain their image until cleanup.",
                      [w.image.targetId],
                      "Retire image",
                    )
                  ) {
                    await mutate("images.approve", {
                      imageId: w.id,
                      approved: false,
                      retired: true,
                    });
                    await refreshWork();
                  }
                },
                "danger",
              ),
            );
          }
          if (w.image && !w.image.unavailable) {
            const evidence = details("Image evidence", card);
            for (const [key, value] of Object.entries(w.image.binding ?? {}))
              if (typeof value !== "object")
                evidence.append(el("p", `${key}: ${value}`));
          }
          const technical = details("Technical details", card);
          inspect(technical, {
            operationId: w.id,
            request: w.request,
            result: w.result,
          });
          return card;
        });
        if (!visible.length)
          workList.append(
            el("p", "No image builds yet.", { "data-key": "empty" }),
          );
      } finally {
        pending = false;
      }
    }
    await refreshWork();
    poll(refreshWork, 15000);
  }
  browseActions.append(button("Refresh templates", refreshTemplates));
  panels.Browse.append(browseActions, sourceView);
  await refreshTemplates();
});
