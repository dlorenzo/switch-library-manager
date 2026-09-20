// --- UI & IPC Helpers (Electron remote module disabled) -------------------
function safeParse(payload, fallback) {
    try {
        if (!payload) return fallback;
        return JSON.parse(payload);
    } catch (e) {
        console.error("Failed to parse IPC payload", e);
        return fallback;
    }
}
function showMessageBox(options, callback) {
    let title = (options && options.title) ? options.title : "";
    let msg = (options && options.message) ? options.message : "";
    let detail = (options && options.detail) ? options.detail : "";
    let text = [title, msg, detail].filter(Boolean).join("\n\n");

    if (options && options.buttons && options.buttons.length > 1) {
        let choice = window.confirm(text);
        let result = { response: choice ? 0 : 1 };
        if (callback) callback(result);
        return Promise.resolve(result);
    } else {
        window.alert(text);
        let result = { response: 0 };
        if (callback) callback(result);
        return Promise.resolve(result);
    }
}

function openFolderPickerHelper(mode, updateFolderFunc) {
    let picker = document.getElementById("folder-picker-hidden-input");
    if (!picker) {
        picker = document.createElement("input");
        picker.type = "file";
        picker.id = "folder-picker-hidden-input";
        picker.webkitdirectory = true;
        picker.style.display = "none";
        document.body.appendChild(picker);
    }
    picker.onchange = function (e) {
        if (e.target.files && e.target.files.length > 0) {
            let firstFile = e.target.files[0];
            let fullPath = firstFile.path || "";
            if (fullPath) {
                let dirPath = fullPath.substring(0, Math.max(fullPath.lastIndexOf('/'), fullPath.lastIndexOf('\\')));
                if (dirPath) {
                    updateFolderFunc(mode, { canceled: false, filePaths: [dirPath] });
                }
            }
        }
        picker.value = "";
    };
    picker.click();
}

function showItemInFolderHelper(filePath) {
    if (filePath && window.astilectron) {
        astilectron.sendMessage({ name: "showItemInFolder", payload: filePath });
    }
}

// --- Security helpers -------------------------------------------------------
// Title names, DLC names and icon URLs originate from remotely-downloaded data
// (titles.json / versions.json, whose URLs are user-editable) and from NACP
// metadata embedded in scanned game files. None of it is trusted. The table
// formatters below build HTML by string interpolation, so every interpolated
// value MUST be escaped, and any URL used in an attribute must be restricted to
// safe schemes. Without this, a crafted title such as
//   <img src=x onerror=...>
// would execute in the renderer (which, with the remote module enabled, can
// reach Node APIs).

// Escape the five HTML-significant characters so a value can be safely placed
// in element text or inside a double-quoted attribute.
function escapeHtml(value) {
    if (value === null || value === undefined) return "";
    return String(value)
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#39;");
}

// Only allow http/https/data-image URLs for <img src>. Anything else
// (javascript:, vbscript:, file:, etc.) is dropped. The result is still passed
// through escapeHtml before being placed in the attribute.
function sanitizeImageUrl(url) {
    if (!url) return "";
    const trimmed = String(url).trim();
    if (/^https?:\/\//i.test(trimmed)) return trimmed;
    if (/^data:image\//i.test(trimmed)) return trimmed;
    return "";
}

$(function () {

    let state = {
        settings:{},
        keys:false
    };

    let currTable

    // Fluent UI formatter for Title + Thumbnail
    const fluentTitleFormatter = function(cell, formatterParams, onRendered){
        const data = cell.getRow().getData();
        const rawImg = data.icon || (data.Attributes && data.Attributes.bannerUrl) || null;
        const rawTitle = data.name || (data.Attributes && data.Attributes.name) || 'Unknown Title';

        // Untrusted: escape text, restrict + escape the URL.
        const title = escapeHtml(rawTitle);
        const imgSrc = escapeHtml(sanitizeImageUrl(rawImg));

        if (imgSrc) {
            return `<div style="display:flex; align-items:center; gap: 12px; padding: 4px 0;">
                      <img src="${imgSrc}" style="width: 52px; height: 52px; border-radius: 6px; object-fit: cover; box-shadow: 0 2px 6px rgba(0,0,0,0.15);">
                      <div style="font-weight: 600; font-size: 14px; white-space: normal; line-height: 1.3; color: var(--fluent-text, inherit);">${title}</div>
                    </div>`;
        } else {
            return `<div style="font-weight: 600; font-size: 14px; white-space: normal; padding: 4px 0; color: var(--fluent-text, inherit);">${title}</div>`;
        }
    };

    // Fluent UI formatter for File paths
    const fluentFileFormatter = function(cell, formatterParams, onRendered){
        const fullPath = cell.getValue();
        if (!fullPath) return "";

        // Try to split the path by standard slashes to separate filename from directory
        const normalizedPath = fullPath.replace(/\\/g, '/');
        const parts = normalizedPath.split('/');
        
        // Escape for use in both element text and the title="" attribute.
        const fileName = escapeHtml(parts.pop());
        const dirName = escapeHtml(parts.join('/') || "/");

        return `<div style="display:flex; flex-direction:column; justify-content:center; padding: 4px 0; cursor: pointer;">
                  <div style="font-weight: 500; font-size: 13px; color: var(--fluent-text, inherit); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%;" title="${fileName}">${fileName}</div>
                  <div style="font-weight: 400; font-size: 11px; color: #888; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%;" title="${dirName}">${dirName}</div>
                </div>`;
    };

    //handle tabs action
    $('.tabgroup > div').hide();
    // loadTab($('.tabgroup > div:first-of-type'));

    // This will wait for the astilectron namespace to be ready
    document.addEventListener('astilectron-ready', function () {
        
        // Restore Maximized State from backend settings
        astilectron.sendMessage({name: "checkMaximized", payload: ""}, function(message) {
            if (message === "true") {
                try { astilectron.sendMessage({name: "maximizeWindow", payload: ""}); } catch(e){}
            }
        });

        let sendMessage = function (name, payload, callback) {
            astilectron.sendMessage({name: name, payload: payload}, callback)
        };

        sendMessage("loadSettings", "", function (message) {
            state.settings = safeParse(message, {}) || {};
            state.settings.organize_options = state.settings.organize_options || {};

            if(state.settings.hide_missing_games){
                document.getElementById("tab_btns").classList.add("hide_missing_games");
            }

            // Apply Dark Mode from settings
            if(state.settings.dark_mode) {
                document.body.classList.add("bootstrap-dark");
                document.body.classList.remove("bootstrap");
                $('meta[name="color-scheme"]').attr("content", "dark");
                $("#toggle-dark-mode").text("☀️");
            } else {
                document.body.classList.add("bootstrap");
                document.body.classList.remove("bootstrap-dark");
                $('meta[name="color-scheme"]').attr("content", "light");
                $("#toggle-dark-mode").text("🌙");
            }
        });

        sendMessage("isKeysFileAvailable", "", function (message) {
            state.keys = message
        });

        sendMessage("checkUpdate", "", function (message) {
            if (message === "false"){
                return
            }
            showMessageBox({
                type: 'info',
                buttons: ['Ok'],
                defaultId: 0,
                title: 'New update available',
                message: 'There is a new update available, please download from Github',
                detail: 'Visit github.com/firebat20/switch-library-manager/releases to download the latest version.'
            });
        });

        $(".progress-container").show();
        $(".progress-type").text("Downloading latest Switch titles/versions ...");

        sendMessage("updateDB", "", function (message) {
            scanLocalFolder();
        });

        astilectron.onMessage(function (message) {
            // Process message
            // console.log(message)
            let pcg = 0
            if (message.name === "updateProgress") {
                let pp = safeParse(message.payload, null);
                if (!pp) return;
                let count = Number(pp.curr) || 0;
                let total = Number(pp.total) || 0;
                $('.progress-msg').text(String(pp.message || ""));
                if (total > 0){
                    pcg = Math.floor(count / total * 100);
                    if (!isFinite(pcg) || pcg < 0) pcg = 0;
                    if (pcg > 100) pcg = 100;
                    $('.progress-bar').attr('aria-valuenow', pcg);
                    $('.progress-bar').attr('style', 'width:' + Number(pcg) + '%');
                    $('.progress-bar').text(pcg + "%");
                }
                if (pcg === 100){
                    $(".progress-container").hide();
                }else{
                    $(".progress-container").show();
                }
            }
            else if (message.name === "libraryLoaded") {
                state.library = safeParse(message.payload, null);
                if (!state.library || !Array.isArray(state.library.library_data)) {
                    state.library = { library_data: [], issues: [], num_files: 0 };
                }
                loadTab("#library")
            }
            else if (message.name === "missingGames") {
                state.missingGames = safeParse(message.payload, []);
                if (!Array.isArray(state.missingGames)) state.missingGames = [];
                loadTab("#missing")
            }
            else if (message.name === "error") {
                showMessageBox({
                    type: 'error',
                    buttons: ['Ok'],
                    defaultId: 0,
                    title: 'Error',
                    message: 'An unexpected error occurred',
                    detail: message.payload
                });
                state.settings.folder = undefined;
                $(".progress-container").hide();
                loadTab("#library")
            }
            else if (message.name === "rescan") {
                state.library = undefined;
                state.updates = undefined;
                state.dlc = undefined;
                scanLocalFolder(true)
            }
        });

        let openFolderPicker = function (mode) {
            openFolderPickerHelper(mode, updateFolder);
        };

        let scanLocalFolder = function(mode){
            if (!state.settings.folder){
                loadTab("#library")
                return
            }
            //show progress
            $(".progress-container").show();
            $(".progress-type").text("Scanning local library...");

            sendMessage("updateLocalLibrary", ""+mode, (r => {}))
        };

        let updateFolder = function (mode,result) {
            if (result.canceled) {
                console.log("user aborted");
                return
            }
            if (!result.filePaths || !result.filePaths.length){
                return
            }

            if (mode === "add"){
                state.settings.scan_folders = state.settings.scan_folders || []
                if (!state.settings.scan_folders.includes(result.filePaths[0])){
                    state.settings.scan_folders.push(result.filePaths[0]);
                }else{
                    return;
                }

            }else{
                state.settings.folder = result.filePaths[0];
            }
            $('.tabgroup > div').hide();
            console.log("selected folder:"+result.filePaths[0]);
            state.library = undefined;
            state.updates = undefined;
            state.dlc = undefined;
            sendMessage("saveSettings", JSON.stringify(state.settings), scanLocalFolder);
        };


        function loadTab(target) {
            hideCurrentTab();

            $("#tab_btns a[href='" + target + "']").addClass('active');
            $(target).show();

            if (target === "#settings") {
                let settingsHtml = $(target + "Template").render({
                    settings: state.settings,
                    ignore_update_title_ids_str: state.settings.ignore_update_title_ids ? state.settings.ignore_update_title_ids.join('\n') : "",
                    ignore_dlc_title_ids_str: state.settings.ignore_dlc_title_ids ? state.settings.ignore_dlc_title_ids.join('\n') : ""
                });
                $(target).html(settingsHtml);
            } else if (target === "#organize") {
                let html = $(target + "Template").render({folder: state.settings.folder,settings:state.settings})
                $(target).html(html);
            } else if (target === "#updates") {
                if (!state.settings) return;
                if (state.settings.folder && !state.library){
                    return
                }
                if (state.library && !state.updates){
                    sendMessage("missingUpdates", "", (r => {
                        state.updates = safeParse(r, [])
                        loadTab("#updates")
                    }));
                    return
                }
                let html = $(target + "Template").render({folder: state.settings.folder,updates:state.updates})
                $(target).html(html);
                if (state.updates && state.updates.length) {
                    currTable = new Tabulator("#updates-table", {
                        layout:"fitDataStretch",
                        initialSort:[
                            {column:"latest_update_date", dir:"desc"}, //sort by this first
                        ],
                        pagination: "local",
                        paginationSize: state.settings.gui_page_size > 0 ? state.settings.gui_page_size : 100,
                        data: state.updates,
                        columns: [
                            {title: "Game", field: "Attributes.name", headerFilter:"input", formatter:fluentTitleFormatter, width:400},
                            {title: "Type", field: "Meta.type", headerFilter:"input"},
                            {title: "Title ID", headerSort:false, field: "Attributes.id", hozAlign: "right", sorter: "number"},
                            {title: "Local version", headerSort:false, field: "local_update", hozAlign: "right", sorter: "number"},
                            {title: "Available version", headerSort:false, field: "latest_update", hozAlign: "right"},
                            {title: "Update date", headerSort:true, field: "latest_update_date",sorter:"date", sorterParams:{format:"YYYY-MM-DD"}}
                        ],
                    });
                }
            } else if (target === "#dlc") {
                if (!state.settings) return;
                if (state.settings.folder && !state.library){
                    return
                }
                if (state.library && !state.dlc){
                    sendMessage("missingDlc", "", (r => {
                        state.dlc = safeParse(r, [])
                        loadTab("#dlc")
                    }));
                    return
                }
                let html = $(target + "Template").render({folder: state.settings.folder,dlc:state.dlc});
                $(target).html(html);
                if (state.dlc && state.dlc.length) {
                    currTable = new Tabulator("#dlc-table", {
                        layout:"fitDataStretch",
                        initialSort:[
                            {column:"Attributes.name", dir:"asc"}, //sort by this first
                        ],
                        pagination: "local",
                        paginationSize: state.settings.gui_page_size,
                        data: state.dlc,
                        columns: [
                            {title: "Game", field: "Attributes.name", headerFilter:"input",formatter:fluentTitleFormatter, width:400},
                            {title: "# Missing", field: "missing_dlc.length"},
                            {title: "Missing DLC", headerSort:false, field: "missing_dlc",formatter:function(cell, formatterParams, onRendered){
                                    // DLC names are untrusted; escape each one.
                                    let value = ""
                                    const list = cell.getValue() || []
                                    for (var i in list)
                                    {
                                        value +="<div>"+escapeHtml(list[i])+"</div>"
                                    }
                                    return value
                                }}
                        ],
                    });
                }
            } else if (target === "#status") {
                if (state.settings.folder && !state.library){
                    return
                }
                let html = $(target + "Template").render({folder: state.settings.folder,library:state.library ? state.library.issues: undefined,numFiles:state.library ? state.library.num_files:-1});
                $(target).html(html);
                if (state.library.issues && state.library.issues.length) {
                    currTable = new Tabulator("#status-table", {
                        layout:"fitDataStretch",
                        pagination: "local",
                        paginationSize: state.settings.gui_page_size,
                        data: state.library.issues,
                        initialSort:[
                            {column:"type", dir:"asc"}, // Sort by issue type by default
                        ],
                        columns: [
                            {title: "Type", field: "type", width: 140, hozAlign: "center", headerSort: true, formatter: function(cell) {
                                const type = cell.getValue();
                                let bg = "rgba(0,0,0,0.05)";
                                let color = "#333";
                                let text = "Unknown";
                                
                                // Dark mode check for pill background
                                const isDark = document.body.classList.contains('bootstrap-dark');
                                
                                switch (type) {
                                    case 0: 
                                        text = "Unsupported"; 
                                        bg = isDark ? "rgba(255,255,255,0.1)" : "#f3f2f1";
                                        color = isDark ? "#ccc" : "#555";
                                        break;
                                    case 1: 
                                        text = "Duplicate"; 
                                        bg = isDark ? "rgba(0, 120, 212, 0.2)" : "rgba(0, 120, 212, 0.1)"; 
                                        color = isDark ? "#6CB8F6" : "#0078D4"; // Fluent Blue
                                        break;
                                    case 2: 
                                        text = "Obsolete"; 
                                        bg = isDark ? "rgba(209, 52, 56, 0.2)" : "rgba(232, 17, 35, 0.1)";
                                        color = isDark ? "#FF99A4" : "#E81123"; // Fluent Red
                                        break;
                                    case 3: 
                                        text = "Unrecognised"; 
                                        bg = isDark ? "rgba(247, 99, 12, 0.2)" : "rgba(247, 99, 12, 0.1)";
                                        color = isDark ? "#FCE100" : "#9D5D00"; // Fluent Orange
                                        break;
                                    case 4: 
                                        text = "Malformed"; 
                                        bg = isDark ? "rgba(209, 52, 56, 0.2)" : "rgba(232, 17, 35, 0.1)";
                                        color = isDark ? "#FF99A4" : "#E81123"; // Fluent Red
                                        break;
                                    case 5: 
                                        text = "Missing Base"; 
                                        bg = isDark ? "rgba(209, 52, 56, 0.2)" : "rgba(232, 17, 35, 0.1)";
                                        color = isDark ? "#FF99A4" : "#E81123"; // Fluent Red
                                        break;
                                }
                                return `<div style="background-color: ${bg}; color: ${color}; font-size: 12px; font-weight: 600; padding: 4px 8px; border-radius: 12px; display: inline-block; line-height: 1;">${text}</div>`;
                            }},
                            {title: "File name",width:500, headerSort:false, field: "key",formatter:fluentFileFormatter,cellClick:function(e, cell){
                                    //e - the click event object
                                    //cell - cell component
                                    showItemInFolderHelper(cell.getData().key)
                                }
                            },
                            {
                                title: "Issue", field: "value", formatter: function (cell) {
                                    // The issue text embeds file paths (untrusted). Escape first,
                                    // THEN turn the known literal markers into markup, so escaping
                                    // can't neutralise our own <br/>/<strong> tags.
                                    return escapeHtml(cell.getValue())
                                        .replaceAll("\nNew: ", "<br/><strong style='color:#0078D4; margin-top:8px; display:inline-block'>New:</strong> ")
                                        .replaceAll("\nOld: ", "<br/><strong style='color:#E81123; margin-top:4px; display:inline-block'>Old:</strong> ")
                                        .replaceAll("\nExisting: ", "<br/><strong style='color:#0078D4; margin-top:8px; display:inline-block'>Existing:</strong> ")
                                        .replaceAll("\nDuplicate: ", "<br/><strong style='color:#E81123; margin-top:4px; display:inline-block'>Duplicate:</strong> ")
                                        .replaceAll("\n", "<br/>");
                                }
                            }
                        ],
                    });
                }
            } else if (target === "#library") {
                if (state.settings.folder && !state.library){
                    return
                }
                let html = $(target + "Template").render(
                    {
                        folder: state.settings.folder,
                        library: state.library ? state.library.library_data : [] ,
                        num_skipped:state.library ? (state.library.issues ? state.library.issues.length : 0) : 0,
                        num_files:state.library ? state.library.num_files : 0,
                        keys:state.keys,
                        scanFolders:state.settings.scan_folders
                    })
                $(target).html(html);
                if (state.library && state.library.library_data.length) {
                    currTable = new Tabulator("#library-table", {
                        initialSort:[
                            {column:"name", dir:"asc"}, //sort by this first
                        ],
                        layout:"fitDataStretch",
                        pagination: "local",
                        paginationSize: state.settings.gui_page_size,
                        data: state.library.library_data,
                        columns: [
                            {title: "Game", field: "name", headerFilter:"input", formatter:fluentTitleFormatter, width:400},
                            {title: "Title ID", headerSort:false, field: "titleId"},
                            {title: "Region", headerSort:true, field: "region"},
                            {title: "Type", headerSort:true, field: "type"},
                            {title: "Update", headerSort:false, field: "update"},
                            {title: "Version", headerSort:false, field: "version"},
                            {title: "File name", headerSort:false, field: "path",formatter:fluentFileFormatter,cellClick:function(e, cell){
                                    //e - the click event object
                                    //cell - cell component
                                    showItemInFolderHelper(cell.getData().path)
                                }
                            }
                        ],
                    });
                }
            } else if (target === "#missing") {
                if (state.settings.folder && !state.library){
                    return
                }
                if (state.library && !state.missingGames){
                    sendMessage("missingGames", "", (r => {
                        state.missingGames = safeParse(r, [])
                        loadTab("#missing")
                    }));
                    return
                }
                let html = $(target + "Template").render({folder: state.settings.folder,missingGames:state.missingGames});
                $(target).html(html);
                if (state.missingGames && state.missingGames.length) {
                    currTable = new Tabulator("#missingGames-table", {
                        layout:"fitDataStretch",
                        initialSort:[
                            {column:"name", dir:"asc"}, //sort by this first
                        ],
                        pagination: "local",
                        paginationSize: state.settings.gui_page_size,
                        data: state.missingGames,
                        columns: [
                            {field: "name", title: "Game", headerFilter:"input", formatter:fluentTitleFormatter, width:400},
                            {title: "Title ID", headerSort:false, field: "titleId"},
                            {title: "Region", headerSort:true,headerFilter:"input",formatter:"textarea", field: "region"},
                            {title: "Release date", headerSort:true, field: "release_date", sorter:"date", sorterParams:{format:"YYYY-MM-DD"}},
                        ],
                    });
                }
            }
        }

        $("body").on("click", ".folder-set", e => {
            openFolderPicker(e.target.textContent.toLowerCase().trim())
        });

        $("body").on("click", ".export-btn", e => {
            currTable.download("csv", "export.csv", {}, "all");
        });

        // Settings Form Submit
        $("body").on("submit", "#settings-form", function(e) {
            e.preventDefault();
            const formData = new FormData(this);
            
            state.settings.prod_keys = formData.get("prod_keys");
            state.settings.gui_page_size = parseInt(formData.get("gui_page_size"));
            state.settings.scan_recursively = formData.has("scan_recursively");
            state.settings.debug = formData.has("debug");
            
            state.settings.check_for_missing_updates = formData.has("check_for_missing_updates");
            state.settings.check_for_missing_dlc = formData.has("check_for_missing_dlc");
            state.settings.hide_missing_games = formData.has("hide_missing_games");
            state.settings.hide_demo_games = formData.has("hide_demo_games");
            state.settings.ignore_dlc_updates = formData.has("ignore_dlc_updates");
            
            state.settings.titles_json_url = formData.get("titles_json_url");
            state.settings.versions_json_url = formData.get("versions_json_url");
            
            const splitComma = (val) => val ? val.split(',').map(s => s.trim()).filter(s => s) : [];
            const splitNewline = (val) => val ? val.split(/\r?\n/).map(s => s.trim()).filter(s => s) : [];
            
            state.settings.ignore_file_types = splitComma(formData.get("ignore_file_types"));
            state.settings.ignore_update_title_ids = splitNewline(formData.get("ignore_update_title_ids"));
            state.settings.ignore_dlc_title_ids = splitNewline(formData.get("ignore_dlc_title_ids"));
            
            const btn = $(this).find("button[type='submit']");
            const originalText = btn.text();
            btn.text("Saving...").prop("disabled", true);
            
            sendMessage("saveSettings", JSON.stringify(state.settings), function() {
                btn.text("Saved!").css({"background-color": "#107C10", "color": "white", "border-color": "transparent"});
                setTimeout(() => {
                    btn.text(originalText).css({"background-color": "", "color": "", "border-color": ""}).prop("disabled", false);
                }, 2000);
                
                if(state.settings.hide_missing_games){
                    document.getElementById("tab_btns").classList.add("hide_missing_games");
                } else {
                    document.getElementById("tab_btns").classList.remove("hide_missing_games");
                }
            });
        });

        // Organize Form Submit
        $("body").on("submit", "#organize-form", function(e) {
            e.preventDefault();
            const formData = new FormData(this);
            
            state.settings.organize_options.create_folder_per_game = formData.has("create_folder_per_game");
            state.settings.organize_options.rename_files = formData.has("rename_files");
            state.settings.organize_options.delete_empty_folders = formData.has("delete_empty_folders");
            state.settings.organize_options.delete_old_update_files = formData.has("delete_old_update_files");
            state.settings.organize_options.process_when_missing_base_game = formData.has("process_when_missing_base_game");
            state.settings.organize_options.switch_safe_file_names = formData.has("switch_safe_file_names");
            state.settings.organize_options.prioritize_compressed = formData.has("prioritize_compressed");
            
            state.settings.organize_options.folder_name_template = formData.get("folder_name_template");
            state.settings.organize_options.file_name_template = formData.get("file_name_template");
            state.settings.organize_options.updates_folder = formData.get("updates_folder");
            state.settings.organize_options.dlc_folder = formData.get("dlc_folder");
            
            sendMessage("saveSettings", JSON.stringify(state.settings), function() {
                if (state.settings.organize_options.create_folder_per_game === false &&
                    state.settings.organize_options.rename_files === false){
                    showMessageBox({
                        title: 'Library organization is turned off',
                        message: 'Both rename files and create folders are disabled.',
                        detail: "You must enable at least one of these options to organize."
                    });
                    return;
                }

                const options = {
                    type: 'warning',
                    buttons: ['Yes', 'No'],
                    defaultId: 0,
                    title: 'Confirmation',
                    message: 'Are you sure you want to begin library organization?',
                    detail: 'This action will modify your local library files based on the settings you just chose.',
                };

                showMessageBox(options).then( (r) => {
                    if (r.response === 0) {
                        $('.tabgroup > div').hide();
                        $(".progress-container").show();
                        $(".progress-type").text("Organizing local library...");

                        sendMessage("organize", "", (r => {
                            $(".progress-container").hide();
                            state.library = undefined;
                            state.updates = undefined;
                            state.dlc = undefined;
                            loadTab("#library");
                            scanLocalFolder(true);
                            showMessageBox({
                                title: 'Success',
                                message: 'Operation completed successfully'
                            });
                        }));
                    }
                });
            });
        });

        // Library & Issues Tab Organize Buttons
        $("body").on("click", ".library-organize-action", e => {
            e.preventDefault();
            if (state.settings.organize_options.create_folder_per_game === false &&
                state.settings.organize_options.rename_files === false){
                showMessageBox({
                    title: 'Library organization is turned off',
                    message: 'Both rename files and create folders are disabled.',
                    detail: "You must enable at least one of these options in the Organize tab to proceed."
                });
                return;
            }

            const options = {
                type: 'warning',
                buttons: ['Yes', 'No'],
                defaultId: 0,
                title: 'Confirmation',
                message: 'Are you sure you want to begin library organization?',
                detail: 'This action will modify your local library files based on your current settings.',
            };

            showMessageBox(options).then( (r) => {
                if (r.response === 0) {
                    $('.tabgroup > div').hide();
                    $(".progress-container").show();
                    $(".progress-type").text("Organizing local library...");

                    sendMessage("organize", "", (r => {
                        $(".progress-container").hide();
                        state.library = undefined;
                        state.updates = undefined;
                        state.dlc = undefined;
                        loadTab("#library");
                        scanLocalFolder(true);
                        showMessageBox({
                            title: 'Success',
                            message: 'Operation completed successfully'
                        });
                    }));
                }
            });
        });

        // Dark Mode Toggle
        $("body").on("click", "#toggle-dark-mode", e => {
            e.preventDefault();
            state.settings.dark_mode = !state.settings.dark_mode;
            
            if(state.settings.dark_mode) {
                document.body.classList.add("bootstrap-dark");
                document.body.classList.remove("bootstrap");
                // nativeTheme handled via CSS class toggle above
                $('meta[name="color-scheme"]').attr("content", "dark");
                $("#toggle-dark-mode").text("☀️");
            } else {
                document.body.classList.add("bootstrap");
                document.body.classList.remove("bootstrap-dark");
                // nativeTheme handled via CSS class toggle above
                $('meta[name="color-scheme"]').attr("content", "light");
                $("#toggle-dark-mode").text("🌙");
            }
            
            // Save the toggle preference without scanning
            sendMessage("saveSettings", JSON.stringify(state.settings), function(){});
        });

        // Rescan Library Toggle
        $("body").on("click", "#btn-rescan", e => {
            e.preventDefault();
            state.library = undefined;
            state.updates = undefined;
            state.dlc = undefined;
            scanLocalFolder(true);
        });

        // Hard Rescan Toggle
        $("body").on("click", "#btn-hard-rescan", e => {
            e.preventDefault();
            const options = {
                type: 'warning',
                buttons: ['Yes', 'No'],
                defaultId: 0,
                title: 'Confirmation',
                message: 'Are you sure you want to perform a Hard Rescan?',
                detail: 'This will completely clear the local database cache and do a deep scan of all your files again. It will take longer than a normal rescan.',
            };
            showMessageBox(options).then( (r) => {
                if (r.response === 0) {
                    sendMessage("hardRescan", "", function(){});
                }
            });
        });



        $('#tab_btns a').click(function (e) {
            e.preventDefault();
            let target = $(e.currentTarget).attr('href');
            if (target === "#") return; // Ignore icon buttons in navbar
            loadTab(target);
        });

        function hideCurrentTab() {
            $("#tab_btns a").removeClass("active");
            let tabgroup = $("#tab_btns").data('tabgroup');
            $("#" + tabgroup).children('div').hide();
        }

        function partial(func /*, 0..n args */) {
            var args = Array.prototype.slice.call(arguments, 1);
            return function() {
                var allArguments = args.concat(Array.prototype.slice.call(arguments));
                return func.apply(this, allArguments);
            };
        }

        // Track Window Dimensions
        let resizeTimer;
        window.addEventListener('resize', () => {
            clearTimeout(resizeTimer);
            resizeTimer = setTimeout(() => {
                // Use standard window properties instead of electron.remote.getCurrentWindow()
                state.settings.window_width = window.innerWidth;
                state.settings.window_height = window.innerHeight;
                sendMessage("saveSettings", JSON.stringify(state.settings), function(){});
            }, 1000); // Save bounds 1 second after user finishes resizing
        });

    });

});