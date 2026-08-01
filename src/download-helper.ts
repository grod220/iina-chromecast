const { utils, http, global, console: IINAConsole } = iina;

const URL = `https://github.com/dpacmittal/iina-chromecast/releases/latest/download/casthelper`;

const casthelperPath = utils.resolvePath("@data/casthelper");

export async function downloadCastHelper() {
  if (iina.file.exists(casthelperPath)) {
    return;
  }
  iina.core.osd("Downloading chromecast helper...");
  await http.download(URL, casthelperPath);
  iina.core.osd("Downloading chromecast helper finished successfully");
}
